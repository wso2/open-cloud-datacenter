
"""
SSH access to a VM's guest OS, using the Harvester host node that currently
runs the VM as a jump host.

The only function users of this module need is `exec_command_in_vm`. How to
find the node running the VM, and how to use that node as a jump host, are
internal implementation details (prefixed with `_`).
"""
import os
from io import StringIO
from time import sleep
from datetime import datetime, timedelta

from kubernetes import client
from paramiko import SSHClient, RSAKey, MissingHostKeyPolicy
from paramiko.ssh_exception import SSHException
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.hazmat.backends import default_backend

from constant import (
    KUBEVIRT_API_GROUP, KUBEVIRT_API_VERSION, VIRTUALMACHINEINSTANCE_PLURAL,
    DEFAULT_NAMESPACE, DEFAULT_TIMEOUT
)
from utility.utility import logging

# Fixed SSH user Harvester nodes are always provisioned with
HOST_SSH_USER = "rancher"


def generate_ssh_keypair():
    """Generate a fresh RSA keypair to inject into a VM's cloud-init
    ssh_authorized_keys (public) and use for guest SSH login (private).

    Returns:
        (public_key_openssh, private_key_pem): str, str
    """
    private_key = rsa.generate_private_key(
        public_exponent=65537, key_size=4096, backend=default_backend()
    )
    private_key_pem = private_key.private_bytes(
        serialization.Encoding.PEM,
        serialization.PrivateFormat.OpenSSH,
        serialization.NoEncryption()
    )
    public_key_ssh = private_key.public_key().public_bytes(
        serialization.Encoding.OpenSSH, serialization.PublicFormat.OpenSSH
    )
    return public_key_ssh.decode('utf-8'), private_key_pem.decode('utf-8')


def _get_host_private_key():
    """Load the private key used to SSH into the Harvester host node
    (jump host), from the SSH_PRIVATE_KEY environment variable.
    """
    raw_key = os.getenv("SSH_PRIVATE_KEY")
    if not raw_key:
        raise RuntimeError(
            "SSH_PRIVATE_KEY environment variable is not set; it is required "
            "to SSH into the Harvester host node as a jump host."
        )
    return RSAKey.from_private_key(StringIO(raw_key))


def _get_vm_node_and_ip(vm_name, network="default", namespace=DEFAULT_NAMESPACE):
    """Return (node_name, vm_ip) for the node currently running the VM and
    the VM's own IP address on `network`.
    """
    obj_api = client.CustomObjectsApi()
    vmi = obj_api.get_namespaced_custom_object(
        group=KUBEVIRT_API_GROUP,
        version=KUBEVIRT_API_VERSION,
        namespace=namespace,
        plural=VIRTUALMACHINEINSTANCE_PLURAL,
        name=vm_name
    )

    node_name = vmi.get('status', {}).get('nodeName')
    # status.interfaces can be explicitly null (not just missing) before
    # the VM's networking is fully up, and dict.get(key, default) only
    # applies the default when the key is absent -- not when its value is
    # None -- so `or []` is needed to avoid a TypeError iterating over
    # None (instead of the intended AssertionError below).
    interfaces = vmi.get('status', {}).get('interfaces') or []
    vm_ip = next(
        (iface.get('ipAddress') for iface in interfaces if iface.get('name') == network),
        None
    )

    if not node_name or not vm_ip:
        raise AssertionError(
            f"VM {namespace}/{vm_name} does not have a node/IP yet "
            f"(node={node_name}, ip={vm_ip})"
        )

    return node_name, vm_ip


def _get_node_internal_ip(node_name):
    """Resolve the InternalIP address of a node, used as the jump host address."""
    core_api = client.CoreV1Api()
    node = core_api.read_node(name=node_name)

    for addr in (node.status.addresses or []):
        if addr.type == 'InternalIP':
            return addr.address

    raise AssertionError(f"Node {node_name} has no InternalIP address")


def _open_jumphost_connection(host_ip):
    """SSH into the Harvester host node, to be used as a jump host."""
    host_client = SSHClient()
    host_client.set_missing_host_key_policy(MissingHostKeyPolicy())
    host_client.connect(host_ip, username=HOST_SSH_USER, pkey=_get_host_private_key())
    return host_client


def _enable_jumphost_forwarding(host_client):
    """Harvester host nodes have `AllowTcpForwarding no` /
    `AllowAgentForwarding no` in effect, which makes sshd reject
    `direct-tcpip` channel requests with "Administratively prohibited" --
    i.e. jump-hosting through them is disabled by default. Comment out
    those directives in place with `sed -i` and restart sshd so the
    change takes effect. This is left enabled on the host afterwards
    (never restored back to disabled) -- see `exec_command_in_vm` for why.

    The directives may be set in the main /etc/ssh/sshd_config, and/or in
    an Include'd drop-in file under /etc/ssh/sshd_config.d/*.conf (the
    default layout on modern distributions/images) -- some hosts don't
    even have a main sshd_config file at all and define everything via
    drop-ins. So every file that actually contains the directive is
    discovered (via grep -rl) and patched, rather than assuming it's
    /etc/ssh/sshd_config.

    The *effective*, sshd-resolved value (via `sshd -T`) is used to decide
    whether anything needs to change and to verify the change afterwards,
    since that's what actually determines whether direct-tcpip channels
    are allowed -- independent of exactly which file(s) the directive
    lives in.

    A no-op (no sed/restart) if the effective config is already in the
    desired state, since this is called around every guest command and
    unnecessary sshd restarts would otherwise be triggered repeatedly
    (e.g. by poll loops such as `wait_for_vm_cloudinit_done`).

    Returns:
        bool: True if sshd was actually restarted (config changed), False
        if it was already in the desired state (no-op). Important for
        callers: restarting sshd only affects *new* incoming connections
        -- an already-established SSH session (like `host_client` here)
        keeps being served by the sshd child process it originally forked
        with, which loaded the *old* config at connect time and keeps
        enforcing it for the lifetime of that session, regardless of the
        restart. So if this returns True, the caller must reconnect (open
        a fresh session) before the new policy will actually apply to any
        tunneling done over that connection.
    """
    directive_re = r'Allow(Tcp|Agent)Forwarding[[:space:]]+no'
    sed_expr = f's/^([[:space:]]*)({directive_re})[[:space:]]*$/\\1#\\2/I'

    # DEBUG: identify which host this is running against, since the bug
    # we're chasing only reproduces on some hosts and not others.
    _, dbg_host, _ = host_client.exec_command("hostname")
    logging(f"[jumphost_policy] running against host: "
            f"{dbg_host.read().decode().strip()}")

    def effective_forwarding_allowed():
        _, out, _ = host_client.exec_command("sudo sshd -T 2>&1 | grep -i forwarding")
        text = out.read().decode()
        logging(f"[jumphost_policy] effective (sshd -T) forwarding values: "
                f"{text.strip() or '(none found)'}")
        allowed = (
            "allowtcpforwarding yes" in text.lower()
            and "allowagentforwarding yes" in text.lower()
        )
        return allowed

    if effective_forwarding_allowed():
        return False

    # DEBUG: dump the raw lines the main sshd_config currently has for
    # these directives, so a failure log shows exactly what was (or
    # wasn't) matched, instead of us having to guess blindly.
    _, dbg_out, _ = host_client.exec_command(
        "grep -inE 'Allow(Tcp|Agent)Forwarding' /etc/ssh/sshd_config 2>&1"
    )
    logging(f"[jumphost_policy] current sshd_config forwarding lines: "
            f"{dbg_out.read().decode().strip() or '(none found)'}")
    logging(f"[jumphost_policy] sed_expr={sed_expr!r}")

    # DEBUG: the directive may be set in an Include'd drop-in file (e.g.
    # /etc/ssh/sshd_config.d/*.conf) rather than the main sshd_config.
    # List any such drop-ins and their forwarding-related lines so we can
    # tell if that's happening.
    _, dbg_dropins, _ = host_client.exec_command(
        "ls -la /etc/ssh/sshd_config.d/ 2>&1"
    )
    logging(f"[jumphost_policy] /etc/ssh/sshd_config.d/ listing: "
            f"{dbg_dropins.read().decode().strip()}")
    _, dbg_dropin_lines, _ = host_client.exec_command(
        "grep -inE 'Allow(Tcp|Agent)Forwarding' /etc/ssh/sshd_config.d/*.conf 2>&1"
    )
    logging(f"[jumphost_policy] sshd_config.d/*.conf forwarding lines: "
            f"{dbg_dropin_lines.read().decode().strip() or '(none found)'}")

    # Find every file (main config and/or drop-ins) that actually defines
    # one of these directives, so we patch all of them -- rather than
    # assuming they live in /etc/ssh/sshd_config, which doesn't always
    # even exist.
    _, out, _ = host_client.exec_command(
        "sudo grep -rliE '" + directive_re + "' "
        "/etc/ssh/sshd_config /etc/ssh/sshd_config.d/ 2>/dev/null"
    )
    files = [f for f in out.read().decode().splitlines() if f.strip()]
    logging(f"[jumphost_policy] files containing the directive: {files or '(none)'}")

    if not files:
        raise AssertionError(
            "Could not find any sshd config file defining "
            "AllowTcpForwarding/AllowAgentForwarding to toggle"
        )

    command = "sudo sh -c '" + " && ".join(
        f'sed -i -E "{sed_expr}" "{f}"' for f in files
    ) + " && systemctl restart sshd'"
    _, out, err = host_client.exec_command(command)
    exit_status = out.channel.recv_exit_status()
    err_text = err.read().decode()
    logging(f"[jumphost_policy] command exit_status={exit_status} stderr={err_text!r}")
    if exit_status != 0:
        raise AssertionError(
            f"Could not update/restart sshd for jumphost policy "
            f"(exit {exit_status}): {err_text}"
        )

    # Verify the change actually took effect (belt-and-braces: confirm
    # rather than silently trusting it, since a broken jumphost policy
    # otherwise manifests as a confusing 10-minute "Administratively
    # prohibited" retry loop in the caller).
    if not effective_forwarding_allowed():
        raise AssertionError(
            "sshd forwarding policy was not updated as expected after "
            "restart; forwarding may still be disabled"
        )

    return True


def _connect_to_vm_via_jumphost(
    host_client, vm_ip, username, pkey=None, timeout=DEFAULT_TIMEOUT
):
    """Open an SSH connection to the VM guest, tunneled through `host_client`."""
    vm_pkey = RSAKey.from_private_key(StringIO(pkey)) if pkey else None

    vm_client = SSHClient()
    vm_client.set_missing_host_key_policy(MissingHostKeyPolicy())

    login_ex = None
    endtime = datetime.now() + timedelta(seconds=timeout)
    while endtime > datetime.now():
        try:
            transport = host_client.get_transport()
            channel = transport.open_channel(
                'direct-tcpip', (vm_ip, 22), transport.sock.getpeername()
            )
            vm_client.connect(vm_ip, username=username, pkey=vm_pkey, sock=channel)
        except SSHException as e:
            # Covers transient conditions while the guest is still coming
            # up: routing/refused-connection failures (ChannelException,
            # NoValidConnectionsError) before sshd is listening, as well as
            # sshd being up but PAM still rejecting logins (e.g. "System is
            # booting up. Unprivileged users are not permitted to log in
            # yet."), which paramiko can surface as a plain
            # SSHException("No existing session") rather than one of those
            # two subclasses.
            login_ex = e
            sleep(3)
        else:
            break
    else:
        raise AssertionError(f"Unable to SSH into VM at {vm_ip}") from login_ex

    return vm_client


def exec_command_in_vm(
    vm_name, command, username, pkey=None, network="default",
    namespace=DEFAULT_NAMESPACE, timeout=DEFAULT_TIMEOUT
):
    """Execute `command` inside a VM's guest OS over SSH.

    Internally this:
      1. Finds which Harvester host node currently runs the VM.
      2. SSHes into that host node (jump host) using SSH_PRIVATE_KEY.
      3. Ensures TCP forwarding is allowed on the host's sshd (disabled by
         default), so it can be used as a jump host. This is left enabled
         on the host afterwards (not restored back to disabled) -- toggling
         it off after every call would just force another sshd restart (and
         another reconnect, see below) the next time any test needs to SSH
         into a VM on that same host, for no real benefit.
         If enabling requires an sshd restart, the jump-host connection is
         reopened afterwards -- restarting sshd only affects *new*
         connections, so the session already in use here would otherwise
         keep enforcing the old, forwarding-disabled config for its entire
         lifetime.
      4. Tunnels an SSH connection through the host into the VM's guest OS.
      5. Runs `command` in the guest and returns its output.

    Arguments:
        vm_name: name of the VM
        command: shell command to run inside the VM guest
        username: SSH username for the VM guest OS (e.g. 'ubuntu', 'opensuse')
        pkey: SSH private key (PEM string) for the VM guest OS
        network: VM network interface name to read the IP address from
        namespace: k8s namespace of the VM
        timeout: seconds to wait for the VM to become reachable through the jump host

    Returns:
        output: str, combined stdout and stderr (stdout followed by stderr)
    """
    node_name, vm_ip = _get_vm_node_and_ip(vm_name, network, namespace)
    host_ip = _get_node_internal_ip(node_name)

    logging(f"Executing command in VM {vm_name} ({vm_ip}) via host {node_name} ({host_ip})")

    host_client = _open_jumphost_connection(host_ip)
    try:
        restarted = _enable_jumphost_forwarding(host_client)
        if restarted:
            # The connection above was served by an sshd child process
            # that loaded the *old* (forwarding-disabled) config at
            # connect time; restarting sshd doesn't affect it. Reconnect
            # so the new session is handled by a freshly forked child
            # that picks up the just-applied config.
            logging("[jumphost_policy] sshd was restarted; reconnecting "
                    "jump-host session to pick up the new config")
            host_client.close()
            host_client = _open_jumphost_connection(host_ip)

        vm_client = _connect_to_vm_via_jumphost(
            host_client, vm_ip, username, pkey, timeout
        )
        try:
            _, out, err = vm_client.exec_command(command)
            return out.read().decode() + err.read().decode()
        finally:
            vm_client.close()
    finally:
        host_client.close()
