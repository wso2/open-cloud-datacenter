from tempfile import NamedTemporaryFile
from unittest import TestCase, mock
from json.decoder import JSONDecodeError

from harvester_api.api import HarvesterAPI
from harvester_api.managers import (
    DEFAULT_NAMESPACE, HostManager, ImageManager,
    KeypairManager, NetworkManager
)
from harvester_api.managers.base import merge_dict, BaseManager


class BaseTestCase(TestCase):
    manager_cls = None

    def setUp(self):
        self.api = mock.MagicMock(sepc=HarvesterAPI)
        self.mgr = self.manager_cls(self.api)
        self.API_VERSION = "TEST_API_VERSION"
        self.api.API_VERSION = self.API_VERSION

    def tearDown(self):
        self.api.reset_mock()


class TestBaseManager(BaseTestCase):
    manager_cls = BaseManager

    def test_weakref(self):
        class FakeAPI:
            pass

        api = FakeAPI()
        bm = BaseManager(api)

        self.assertEqual(bm.api, api)

        with self.assertRaises(ReferenceError):
            del api
            bm.api

    def test__inject_data(self):
        raw_data = dict(some="{API_VERSION}",
                        thing=42,
                        api="unknown/{API_VERSION}")

        data = self.mgr._inject_data(raw_data)

        self.assertEqual(raw_data.keys(), data.keys())
        self.assertEqual(data['some'], self.api.API_VERSION)
        self.assertEqual(data['api'], "unknown/%s" % self.api.API_VERSION)

    def test__delegate(self):
        m_resp = mock.MagicMock(headers={"Content-Type": "json"})
        self.api._get.return_value = m_resp

        # Case 1: general case
        resp = self.mgr._delegate("_get", "/test/path")

        self.assertEqual(resp, (m_resp.status_code, m_resp.json.return_value))

        # Case 2: raw response
        m_resp.reset_mock()

        resp = self.mgr._delegate("_get", "/test/path", raw=True)

        self.assertEqual(resp, m_resp)

        # Case 3: json decode error
        m_resp.reset_mock()

        exception = JSONDecodeError("test Error", doc="", pos=42)
        m_resp.json.side_effect = exception
        resp = self.mgr._delegate("_get", "/test/path")

        self.assertEqual(resp, (m_resp.status_code,
                                dict(error=exception, response=m_resp)))

    def test__update(self):
        path, data = "/test/path", dict(test="data")

        # Case 1: default
        self.mgr._update(path, data)

        self.assertIn(path, self.api._put.call_args[0][0])
        self.assertDictEqual(dict(json=data), self.api._put.call_args[1])

        # Case 2: as_json = False
        self.mgr._update(path, data, as_json=False)

        self.assertDictEqual(dict(data=data), self.api._put.call_args[1])

        # Case 3: keyword argument overwrite
        self.mgr._update(path, data, json=dict())

        self.assertDictEqual(dict(json=data), self.api._put.call_args[1])

    def test_for_version(self):
        class B0(BaseManager):
            pass

        class B103(B0):
            support_to = '1.0.3'

        class B110(B0):
            support_to = '1.1.0'

        class B120(B0):
            support_to = '1.2.0'

        class B113(B120):
            support_to = '1.1.3'

        class B130(B120):
            support_to = '1.3.0'

        class B114(B130):
            support_to = '1.1.4'

        self.assertEqual(B0.for_version('1.0.3'), B103)
        self.assertEqual(B0.for_version('1.1.0'), B110)
        self.assertEqual(B0.for_version('1.2.0'), B120)
        self.assertEqual(B0.for_version('1.1.3'), B113)
        self.assertEqual(B0.for_version('1.3.0'), B130)
        self.assertEqual(B0.for_version('1.1.4'), B114)


class TestHostManager(BaseTestCase):
    manager_cls = HostManager

    def test_create(self):
        with self.assertRaises(NotImplementedError):
            self.mgr.create()

    def test_get(self):
        # Case 1: specific node
        node_name = "called"
        self.mgr.get(node_name)

        self.assertIn(node_name, self.api._get.call_args[0][0])

        # Case 2: all
        self.mgr.get()
        self.assertTrue(self.api._get.call_args[0][0].endswith("/"))

    def test_delete(self):
        node_name = "called"
        self.mgr.delete(node_name)

        self.assertIn(node_name, self.api._delete.call_args[0][0])

    def test_update(self):
        node_name, data = "called", dict(metadata=dict(nothing=True))
        stub = dict(metadata=dict(something=False))
        self.api._get().json.return_value = stub

        # Case 1: passing as raw data
        self.mgr.update(node_name, data, as_json=False)

        self.assertIn(node_name, self.api._put.call_args[0][0])
        self.assertDictEqual(dict(data=data), self.api._put.call_args[1])

        # Case 2: passing as JSON with data (from get) merged
        self.mgr.update(node_name, data, as_json=True)

        self.assertIn(node_name, self.api._put.call_args[0][0])
        self.assertDictEqual(dict(json=merge_dict(data, stub)), self.api._put.call_args[1])

    def test_get_metrics(self):
        # Case 1: specific node
        node_name = "called"
        self.mgr.get_metrics(node_name)

        self.assertIn(node_name, self.api._get.call_args[0][0])

        # Case 2: all
        self.mgr.get_metrics()

        self.assertTrue(self.api._get.call_args[0][0].endswith("/"))

    def test_maintenance_mode(self):
        # Case 1: Enable
        node_name, enable = "dummy", "enableMaintenanceMode"
        self.mgr.maintenance_mode(node_name)

        self.assertIn(node_name, self.api._post.call_args[0][0])
        self.assertDictEqual(dict(params=dict(action=enable)), self.api._post.call_args[1])

        # Case 2: Disable
        disable = "disableMaintenanceMode"
        self.mgr.maintenance_mode(node_name, False)

        self.assertIn(node_name, self.api._post.call_args[0][0])
        self.assertDictEqual(dict(params=dict(action=disable)), self.api._post.call_args[1])


class TestImageManager(BaseTestCase):
    manager_cls = ImageManager

    def test_get(self):
        name, namespace = "image", "the-namespace"

        # Case 1: default
        self.mgr.get(name)

        self.assertIn(name, self.api._get.call_args[0][0])
        self.assertIn(DEFAULT_NAMESPACE, self.api._get.call_args[0][0])

        # Case 2: specific namespace
        self.mgr.get(name, namespace)

        self.assertIn(name, self.api._get.call_args[0][0])
        self.assertIn(namespace, self.api._get.call_args[0][0])

    def test_create_data(self):
        name, url, desc, stype = "name", "url", "desc", "stype"
        namespace, display_name = "namespace", "displayName"

        # Case 1: display name not assigned
        data = self.mgr.create_data(name, url, desc, stype, namespace)

        self.assertEqual(name, data['metadata']['name'])
        self.assertEqual(url, data['spec']['url'])
        self.assertEqual(desc, data['metadata']['annotations']['field.cattle.io/description'])
        self.assertEqual(stype, data['spec']['sourceType'])
        self.assertEqual(namespace, data['metadata']['namespace'])
        self.assertEqual(name, data['spec']['displayName'])
        self.assertEqual(self.API_VERSION, data['apiVersion'])

        # Case 2: display name assigned

        data = self.mgr.create_data(name, url, desc, stype, namespace, display_name)

        self.assertEqual(display_name, data['spec']['displayName'])

    def test_create_by_url(self):
        name, url, namespace = "ImageName", "testURL", "TestNamespace"

        # Case 1: default namespace
        self.mgr.create_by_url(name, url)

        self.assertNotIn(name, self.api._post.call_args[0][0])
        self.assertIn(DEFAULT_NAMESPACE, self.api._post.call_args[0][0])

        # Case 2: specific namespace
        self.mgr.create_by_url(name, url, namespace=namespace)

        self.assertIn(namespace, self.api._post.call_args[0][0])

    def test_create_by_file(self):
        name, namespace = "ImageName", "TestNamespace"

        with NamedTemporaryFile() as file:
            # Case 1: default namespace
            self.mgr.create_by_file(name, file.name)
            self.assertIn(name, self.api._post.call_args[0][0])
            self.assertIn(DEFAULT_NAMESPACE, self.api._post.call_args[0][0])

            # Case 2: specific namespace
            self.mgr.create_by_file(name, file.name, namespace)
            self.assertIn(name, self.api._post.call_args[0][0])
            self.assertIn(namespace, self.api._post.call_args[0][0])

    def test_update(self):
        name, namespace = "TestImageName", "TestNamespace"
        data = dict(metadata=dict(namespace=namespace))
        self.api._get().json.return_value = dict()

        # Case 1: namespace miss
        self.mgr.update(name, dict())

        self.assertIn(name, self.api._put.call_args[0][0])
        self.assertNotIn(namespace, self.api._put.call_args[0][0])

        # Case 2: specific namespace
        self.mgr.update(name, data)

        self.assertIn(name, self.api._put.call_args[0][0])
        self.assertIn(namespace, self.api._put.call_args[0][0])

    def test_delete(self):
        name, namespace = "TestImageName", "TestNamespace"

        self.mgr.delete(name, namespace)

        self.assertIn(name, self.api._delete.call_args[0][0])
        self.assertIn(namespace, self.api._delete.call_args[0][0])


class TestKeypairManager(BaseTestCase):
    manager_cls = KeypairManager

    def test_get(self):
        name, namespace = "SSHKEYNAME", "keypair-namespace"

        # Case 1: default
        self.mgr.get(name)

        self.assertIn(name, self.api._get.call_args[0][0])
        self.assertIn(DEFAULT_NAMESPACE, self.api._get.call_args[0][0])

        # Case 2: specific namespace
        self.mgr.get(name, namespace)

        self.assertIn(name, self.api._get.call_args[0][0])
        self.assertIn(namespace, self.api._get.call_args[0][0])

    def test_create_data(self):
        name, namespace, public_key = "SSHKEYNAME", "keypair-namespace", "publicKey"

        data = self.mgr.create_data(name, namespace, public_key)

        self.assertEqual(name, data['metadata']['name'])
        self.assertEqual(namespace, data['metadata']['namespace'])
        self.assertEqual(public_key, data['spec']['publicKey'])
        self.assertEqual(self.API_VERSION, data['apiVersion'])

    def test_create(self):
        name, public_key, namespace = "SSHKEYNAME", "publicKey", "keypair-namespace"

        # Case 1: default namespace
        self.mgr.create(name, public_key)

        self.assertNotIn(name, self.api._post.call_args[0][0])
        self.assertIn(DEFAULT_NAMESPACE, self.api._post.call_args[0][0])

        # Case 2: specific namespace
        self.mgr.create(name, public_key, namespace=namespace)

        self.assertIn(namespace, self.api._post.call_args[0][0])

    def test_delete(self):
        name, namespace = "SSHKEYNAME", "keypair-namespace"

        self.mgr.delete(name, namespace)

        self.assertIn(name, self.api._delete.call_args[0][0])
        self.assertIn(namespace, self.api._delete.call_args[0][0])


class TestNetworkManager(BaseTestCase):
    manager_cls = NetworkManager

    def test_get(self):
        name, namespace = "VLAN_NAME", "VLAN_NS"

        # Case 1: default
        self.mgr.get(name)

        self.assertIn(name, self.api._get.call_args[0][0])
        self.assertIn(DEFAULT_NAMESPACE, self.api._get.call_args[0][0])

        # Case 2: specific namespace
        self.mgr.get(name, namespace)

        self.assertIn(name, self.api._get.call_args[0][0])
        self.assertIn(namespace, self.api._get.call_args[0][0])

    def test_create_data(self):
        name, namespace, vlan_id = "VLAN_NAME", "VLAN_NS", 42

        data = self.mgr.create_data(name, namespace, vlan_id)

        self.assertEqual(name, data['metadata']['name'])
        self.assertEqual(namespace, data['metadata']['namespace'])
        self.assertIn(str(vlan_id), data['spec']['config'])
        self.assertEqual(self.mgr.API_VERSION, data['apiVersion'])

    def test_create(self):
        name, namespace, vlan_id = "VLAN_NAME", "VLAN_NS", 42

        # Case 1: default namespace
        # Case 1: default namespace
        self.mgr.create(name, vlan_id)

        self.assertNotIn(name, self.api._post.call_args[0][0])
        self.assertIn(DEFAULT_NAMESPACE, self.api._post.call_args[0][0])

        # Case 2: specific namespace
        self.mgr.create(name, vlan_id, namespace=namespace)

        self.assertIn(namespace, self.api._post.call_args[0][0])

    def test_delete(self):
        name, namespace = "VLAN_NAME", "VLAN_NS"

        self.mgr.delete(name, namespace)

        self.assertIn(name, self.api._delete.call_args[0][0])
        self.assertIn(namespace, self.api._delete.call_args[0][0])
