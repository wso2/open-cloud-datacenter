
"""
Utility functions for Harvester Robot Framework tests
Includes the shared API client accessor function
"""

import logging as log
import coloredlogs
import os
import re
import sys
from datetime import datetime

# Global API client instance
_harvester_api_client = None

coloredlogs.install(level='INFO', fmt='%(asctime)s %(levelname)s %(message)s')
logger = log.getLogger(__name__)


def get_harvester_api_client():
    """
    Get the shared Harvester API client instance.
    """
    global _harvester_api_client    # NOQA
    if _harvester_api_client is None:
        raise RuntimeError(
            "Harvester API client not initialized. "
            "Call init_harvester_api_client() first."
        )
    caller = sys._getframe(1)
    logging(f"DEBUG: get_harvester_api_client() called \
            from {caller.f_code.co_filename}:{caller.f_lineno}")
    state = 'NONE' if _harvester_api_client is None else 'SET'
    logging(f"DEBUG: _harvester_api_client is {state}")
    return _harvester_api_client


def init_harvester_api_client(endpoint, username, password):
    """Initialize the shared Harvester API client (apiclient/harvester_api)"""
    global _harvester_api_client

    from utility.api_client import RobotHarvesterAPI

    logging(f'Initializing Harvester API client for {endpoint}')

    api_token = os.getenv("RANCHER_API_KEY") or os.getenv("HARVESTER_API_TOKEN")
    endpoint = endpoint.rstrip("/") + "/"

    if api_token:
        authorization = api_token
        if not authorization.lower().startswith("bearer "):
            authorization = f"Bearer {authorization}"
        _harvester_api_client = RobotHarvesterAPI(
            endpoint, token=authorization
        )
        _harvester_api_client.session.verify = False
        _harvester_api_client.load_managers(_harvester_api_client.cluster_version)
    else:
        _harvester_api_client = RobotHarvesterAPI.login(
            endpoint, username, password, ssl_verify=False
        )

    logging('Harvester API client initialized successfully '
            f'(cluster version: {_harvester_api_client.raw_version})')
    return _harvester_api_client


def get_timestamp():
    return datetime.now().strftime("%Y%m%d%H%M%S")


def generate_name_with_suffix(kind, suffix, precise=True):
    """Generate unique name with timestamp suffix
    e.g.
    * %m%d%H%M%S%f -> 0618101519681156 (default; sub-second precision so
      concurrent pabot suites never collide — colliding names also make their
      local teardowns delete each other's resources)
    * %m%d%H%M -> 06181015 (precise=False; ONLY for resources with tight name
      length limits, e.g. cluster networks whose bridge interface name is
      capped at 15 chars — callers opt in and accept the collision risk)
    """
    format_str = "%m%d%H%M%S%f" if precise else "%m%d%H%M"
    timestamp = datetime.now().strftime(format_str)
    return "-".join([s for s in [kind, suffix, timestamp] if s])


def _extract_release(text):
    """Extract a (major, minor, micro) tuple from a version-ish string."""
    match = re.search(r"v?(\d+)\.(\d+)(?:\.(\d+))?", text)
    if match:
        return tuple(int(g or 0) for g in match.groups())
    return None


def get_cluster_version_release():
    """Best-effort (major, minor, micro) tuple of the Harvester cluster.

    Reads the raw server-version setting and extracts the release, so
    CI/dev builds with a non-semver version (e.g.
    release-v1.9.0-rc5-b117-head or v1.8-head) still resolve. Returns
    None for versionless values (master/commit-hash builds) - callers
    should treat those as the newest release.
    """
    api = get_harvester_api_client()
    code, data = api.get("apis/harvesterhci.io/v1beta1/settings/server-version")
    release = _extract_release(data.get('value', '') if code == 200 else '')
    if release is None:
        logging("No release version found in server-version, "
                "treating as the newest release")
    return release


def get_retry_count_and_interval():
    """Get retry count and interval from environment or defaults"""
    retry_count = int(os.getenv('RETRY_COUNT', '100'))
    retry_interval = int(os.getenv('RETRY_INTERVAL', '3'))
    return retry_count, retry_interval


def logging(message, level='INFO'):
    """Log message with specified level"""
    if level == 'DEBUG':
        logger.debug(message)
    elif level == 'INFO':
        logger.info(message)
    elif level == 'WARNING':
        logger.warning(message)
    elif level == 'ERROR':
        logger.error(message)
    else:
        logger.info(message)


def init_k8s_api_client():
    """Initialize Kubernetes API client"""
    # Suppress SSL warnings first
    import urllib3
    urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)

    from kubernetes import client, config

    try:
        config.load_incluster_config()
        logging("Loaded in-cluster Kubernetes config", level="DEBUG")
    except Exception as e:
        msg = f"Loading Kubernetes in-cluster config didn't work, fall back to kube config: {e}"
        logging(msg, level="DEBUG")
        config.load_kube_config()
        logging("Loaded kubeconfig", level="DEBUG")

    # Disable SSL verification for self-signed certificates
    # Get the current configuration and modify it
    configuration = client.Configuration.get_default_copy()
    configuration.verify_ssl = False
    configuration.assert_hostname = False

    # Set it as the default
    client.Configuration.set_default(configuration)

    # Also create and set custom API client with SSL verification disabled
    api_client = client.ApiClient(configuration=configuration)

    logging("Kubernetes API client initialized successfully with SSL verification disabled")

    return api_client
