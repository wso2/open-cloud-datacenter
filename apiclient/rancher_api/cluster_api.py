from urllib.parse import urljoin

from .cluster_managers import (
    ProjectManager, ProjectMemberManager,
    PersistentVolumeManager, StorageClassManager, PersistentVolumeClaimManager
)


class ClusterExploreAPI:
    def __init__(self, endpoint, session, cluster_id):
        self.endpoint = endpoint
        self.cluster_id = cluster_id
        self.session = session

        # Cluster
        self.projects = ProjectManager(self)
        self.project_members = ProjectMemberManager(self)
        # Storage
        self.PersistentVolumes = self.pvs = PersistentVolumeManager(self)
        self.storageclasses = self.scs = StorageClassManager(self)
        self.persistentvolumeclaims = self.pvcs = PersistentVolumeClaimManager(self)

    def imitate(self, manager):
        return type(manager)(self, manager._ver)

    def _get(self, path, *, from_cluster=True, **kwargs):
        if from_cluster:
            path = f"k8s/clusters/{self.cluster_id}/{path}"
        url = urljoin(self.endpoint, path)
        return self.session.get(url, **kwargs)

    def _post(self, path, *, from_cluster=True, **kwargs):
        if from_cluster:
            path = f"k8s/clusters/{self.cluster_id}/{path}"
        url = urljoin(self.endpoint, path)
        return self.session.post(url, **kwargs)

    def _put(self, path, *, from_cluster=True, **kwargs):
        if from_cluster:
            path = f"k8s/clusters/{self.cluster_id}/{path}"
        url = urljoin(self.endpoint, path)
        return self.session.put(url, **kwargs)

    def _delete(self, path, *, from_cluster=True, **kwargs):
        if from_cluster:
            path = f"k8s/clusters/{self.cluster_id}/{path}"
        url = urljoin(self.endpoint, path)
        return self.session.delete(url, **kwargs)
