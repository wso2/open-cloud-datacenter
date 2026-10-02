from .managers import BaseManager, merge_dict
from .cluster_models import PersistentVolumeClaimSpec, ProjectSpec

DEFAULT_NAMESPACE = "default"


class ProjectManager(BaseManager):
    PATH_fmt = "v3/projects/{pid}"
    Spec = ProjectSpec

    def get(self, project_id="", *, raw=False, **kwargs):
        if project_id:
            path = self.PATH_fmt.format(pid=project_id)
            params = dict() or kwargs.pop('params', {})
        else:
            path = self.PATH_fmt.format(pid="")
            params = merge_dict(dict(clusterId=self.api.cluster_id), kwargs.pop('params', {}))
        return self._get(path, from_cluster=False, raw=raw, params=params)

    def get_by_name(self, name, *, raw=False):
        resp = self.get(params=dict(name=name))
        if raw:
            return resp
        try:
            code, data = resp
            return code, data['data'][0]
        except KeyError:
            return code, data
        except IndexError:
            return 404, dict(type="error", status=404, code="notFound",
                             message=f"Failed to find project {name!r}")

    def create(self, name, spec, *, raw=False):
        if isinstance(spec, self.Spec):
            spec = spec.to_dict(name, self.api.cluster_id)
        path = self.PATH_fmt.format(pid="")
        return self._create(path, from_cluster=False, raw=raw, json=spec)

    def delete(self, project_id, *, raw=False):
        path = self.PATH_fmt.format(pid=project_id)
        return self._delete(path, from_cluster=False, raw=raw)


class ProjectMemberManager(BaseManager):
    PATH_fmt = "v3/projectroletemplatebindings/{uid}"

    def get(self, uid="", *, raw=False, **kwargs):
        resp = self._get(self.PATH_fmt.format(uid=uid), from_cluster=False, **kwargs)
        if raw:
            return resp
        try:
            code, data = resp
            cluster_id = self.api.cluster_id
            data['data'] = [d for d in data['data'] if d['projectId'].startswith(cluster_id)]
            return code, data
        except KeyError:
            return code, data

    def get_by_project_id(self, project_id, *, raw=False):
        return self.get(params=dict(projectId=project_id), raw=raw)

    def create(self, project_id, user_pid, role_tid, *, raw=False):
        data = {
            "type": "projectroletemplatebinding",
            "projectId": project_id,
            "roleTemplateId": role_tid,
            "userPrincipalId": user_pid
        }
        return self._create(self.PATH_fmt.format(uid=""), json=data, raw=raw, from_cluster=False)

    def delete(self, uid, *, raw=False):
        return self._delete(self.PATH_fmt.format(uid=uid), raw=raw, from_cluster=False)


class PersistentVolumeManager(BaseManager):
    PATH_fmt = "v1/persistentvolumes/{uid}"

    def get(self, name="", *, raw=False):
        return self._get(self.PATH_fmt.format(uid=name), raw=raw)

    def create(self, *args, **kwargs):
        raise NotImplementedError("Not implemented yet.")

    def update(self, *args, **kwargs):
        raise NotImplementedError("Not implemented yet.")

    def delete(self, name, *, raw=False):
        return self._delete(self.PATH_fmt.format(uid=name))


class StorageClassManager(BaseManager):
    PATH_fmt = "v1/storage.k8s.io.storageclasses/{uid}"
    DEFAULT_KEY = "storageclass.kubernetes.io/is-default-class"

    def get(self, name="", *, raw=False):
        return self._get(self.PATH_fmt.format(uid=name), raw=raw)

    def create(self, *args, **kwargs):
        raise NotImplementedError("Not implemented yet.")

    def update(self, *args, **kwargs):
        raise NotImplementedError("Not implemented yet.")

    def delete(self, name, *, raw=False):
        return self._delete(self.PATH_fmt.format(uid=name))


class PersistentVolumeClaimManager(BaseManager):
    PATH_fmt = "v1/persistentvolumeclaims/{ns}/{uid}"
    Spec = PersistentVolumeClaimSpec

    def get(self, name="", namespace=DEFAULT_NAMESPACE, *, raw=False):
        return self._get(self.PATH_fmt.format(uid=name, ns=namespace), raw=raw)

    def create(self, name, pvc_spec, namespace=DEFAULT_NAMESPACE, volume=None, *, raw=False):
        if isinstance(pvc_spec, self.Spec):
            pvc_spec = pvc_spec.to_dict(name, namespace, volume)

        path = self.PATH_fmt.format(uid="", ns="").rstrip('/')
        return self._create(path, json=pvc_spec, raw=raw)

    def update(self, name, pvc_spec, namespace=DEFAULT_NAMESPACE, *,
               raw=False, as_json=True, **kwargs):
        if isinstance(pvc_spec, self.Spec):
            pvc_spec = pvc_spec.to_dict(name, namespace, None)

        path = self.PATH_fmt.format(uid=name, ns=namespace)
        return self._update(path, pvc_spec, raw=raw, as_json=as_json, **kwargs)

    def delete(self, name, namespace=DEFAULT_NAMESPACE, *, raw=False):
        return self._delete(self.PATH_fmt.format(uid=name, ns=namespace))
