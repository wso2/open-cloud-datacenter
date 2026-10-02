"""
Adapter around the shared Harvester API client (apiclient/harvester_api).

The `harvester_api` package is the same client used by harvester_e2e_tests.
It provides version-aware resource managers (api.vms, api.images, ...) that
return (status_code, data) tuples.

The REST implementations in libs/*/rest.py additionally rely on generic
HTTP verbs returning (status_code, data) tuples for resources the managers
do not cover (e.g. pods, services, addons by raw path). HarvesterAPI only
exposes `_get`/`_post`/... returning raw `requests.Response`, so this
subclass adds tuple-returning `get`/`post`/`put`/`delete` wrappers.
"""

import urllib3

from harvester_api import HarvesterAPI

# Test environments use self-signed certificates
urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)


class RobotHarvesterAPI(HarvesterAPI):
    """HarvesterAPI with tuple-returning generic HTTP verbs."""

    def _verb(self, meth, path, data=None, **kwargs):
        if data is not None:
            kwargs.setdefault("json", data)
        resp = getattr(super(), f"_{meth}")(path, **kwargs)
        try:
            return resp.status_code, resp.json()
        except ValueError:
            return resp.status_code, resp.text

    def get(self, path, **kwargs):
        return self._verb("get", path, **kwargs)

    def post(self, path, data=None, **kwargs):
        return self._verb("post", path, data, **kwargs)

    def put(self, path, data=None, **kwargs):
        return self._verb("put", path, data, **kwargs)

    def delete(self, path, **kwargs):
        return self._verb("delete", path, **kwargs)
