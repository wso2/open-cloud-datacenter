"""
Image CRD Implementation
Uses Kubernetes Custom Resources for VirtualMachineImage operations
"""
import time
from kubernetes import client
from kubernetes.client.rest import ApiException
from crd import (
    get_cr, create_cr, delete_cr, list_cr, patch_cr,
    wait_for_cr_deleted, wait_for_cr_condition
)
from constant import (
    HARVESTER_API_GROUP, HARVESTER_API_VERSION,
    VIRTUALMACHINEIMAGE_PLURAL, DEFAULT_NAMESPACE,
    IMAGE_STATE_ACTIVE, IMAGE_STATE_IMPORTING, IMAGE_STATE_FAILED,
    LABEL_TEST, LABEL_TEST_VALUE,
    DEFAULT_TIMEOUT
)
from image.base import Base
from utility.utility import logging, get_retry_count_and_interval


class CRD(Base):
    """
    Image CRD implementation
    Uses harvesterhci.io/v1beta1 VirtualMachineImage CRD
    """

    def __init__(self):
        self.obj_api = client.CustomObjectsApi()
        self.retry_count, self.retry_interval = get_retry_count_and_interval()

    def create_from_url(self, image_name, image_url, checksum="", **kwargs):
        """
        Create a VirtualMachineImage from URL

        CRD Structure:
        apiVersion: harvesterhci.io/v1beta1
        kind: VirtualMachineImage
        spec:
          displayName: "Display Name"
          sourceType: download
          url: "https://..."
        """
        namespace = kwargs.get('namespace', DEFAULT_NAMESPACE)
        display_name = kwargs.get('display_name', image_name)
        description = kwargs.get('description', f'Test image {image_name}')
        retry = kwargs.get('retry', None)

        body = {
            "apiVersion": f"{HARVESTER_API_GROUP}/{HARVESTER_API_VERSION}",
            "kind": "VirtualMachineImage",
            "metadata": {
                "name": image_name,
                "namespace": namespace,
                "labels": {
                    LABEL_TEST: LABEL_TEST_VALUE
                },
                "annotations": {
                    "field.cattle.io/description": description
                }
            },
            "spec": {
                "displayName": display_name,
                "sourceType": "download",
                "url": image_url
            }
        }

        # Add checksum if provided
        if checksum:
            body["spec"]["checksum"] = checksum

        # spec.retry (import retry limit, cluster default 3). retry=0 makes a
        # doomed import (e.g. bad checksum) fail on the first attempt instead
        # of re-downloading the whole image several times.
        if retry is not None:
            body["spec"]["retry"] = int(retry)

        try:
            logging(f"Creating VirtualMachineImage {namespace}/{image_name}")
            result = create_cr(             # NOQA
                group=HARVESTER_API_GROUP,
                version=HARVESTER_API_VERSION,
                namespace=namespace,
                plural=VIRTUALMACHINEIMAGE_PLURAL,
                body=body
            )

            # Wait for image to be created
            self.wait_for_image_created(image_name, namespace)

            return f"{namespace}/{image_name}"

        except ApiException as e:
            logging(f"Failed to create image {image_name}: {e}")
            raise Exception(f"Failed to create image: {e.status}, {e.reason}")

    def try_create(self, image_name, image_url="", source_type="download",
                   checksum="", namespace=DEFAULT_NAMESPACE):
        """Attempt to create an image for negative testing.

        Never raises on API rejection; returns {success, code, message} so a
        caller can assert on the failure code/reason (e.g. empty url/data).
        """
        body = {
            "apiVersion": f"{HARVESTER_API_GROUP}/{HARVESTER_API_VERSION}",
            "kind": "VirtualMachineImage",
            "metadata": {
                "name": image_name,
                "namespace": namespace,
                "labels": {
                    LABEL_TEST: LABEL_TEST_VALUE
                }
            },
            "spec": {
                "displayName": image_name,
                "sourceType": source_type,
                "url": image_url
            }
        }
        if checksum:
            body["spec"]["checksum"] = checksum

        try:
            create_cr(
                group=HARVESTER_API_GROUP,
                version=HARVESTER_API_VERSION,
                namespace=namespace,
                plural=VIRTUALMACHINEIMAGE_PLURAL,
                body=body
            )
            logging(f"Image {namespace}/{image_name} was unexpectedly created "
                    f"(sourceType='{source_type}', url='{image_url}')", "WARNING")
            return {"success": True, "code": 201, "message": ""}
        except ApiException as e:
            logging(f"Image {namespace}/{image_name} rejected as expected "
                    f"(status={e.status}): {e.reason}")
            return {"success": False, "code": e.status,
                    "message": e.body or e.reason or ""}

    def try_get(self, image_name, namespace=DEFAULT_NAMESPACE):
        """Attempt to get an image; returns {success, code, message}."""
        try:
            self.get(image_name, namespace)
            return {"success": True, "code": 200, "message": ""}
        except ApiException as e:
            return {"success": False, "code": e.status,
                    "message": e.body or e.reason or ""}

    def try_delete(self, image_name, namespace=DEFAULT_NAMESPACE):
        """Attempt to delete an image; returns {success, code, message}.

        Unlike delete(), this does NOT swallow a 404 so callers can assert that
        deleting a missing image is rejected.
        """
        try:
            delete_cr(
                group=HARVESTER_API_GROUP,
                version=HARVESTER_API_VERSION,
                namespace=namespace,
                plural=VIRTUALMACHINEIMAGE_PLURAL,
                name=image_name
            )
            return {"success": True, "code": 200, "message": ""}
        except ApiException as e:
            logging(f"Delete of image {namespace}/{image_name} returned "
                    f"status={e.status}: {e.reason}")
            return {"success": False, "code": e.status,
                    "message": e.body or e.reason or ""}

    def update(self, image_name, metadata, namespace=DEFAULT_NAMESPACE):
        """Patch an image's metadata (labels/annotations). Returns the CR."""
        body = {"metadata": metadata}
        return patch_cr(
            group=HARVESTER_API_GROUP,
            version=HARVESTER_API_VERSION,
            namespace=namespace,
            plural=VIRTUALMACHINEIMAGE_PLURAL,
            name=image_name,
            body=body
        )

    def get_metadata(self, image_name, namespace=DEFAULT_NAMESPACE):
        """Return the metadata block of an image"""
        cr = self.get(image_name, namespace)
        return cr.get("metadata", {})

    def delete(self, image_name, namespace=DEFAULT_NAMESPACE):
        """Delete a VirtualMachineImage"""
        try:
            logging(f"Deleting VirtualMachineImage {namespace}/{image_name}")
            delete_cr(
                group=HARVESTER_API_GROUP,
                version=HARVESTER_API_VERSION,
                namespace=namespace,
                plural=VIRTUALMACHINEIMAGE_PLURAL,
                name=image_name
            )
        except ApiException as e:
            if e.status != 404:  # Ignore not found errors
                logging(f"Error deleting image {image_name}: {e}")
                raise

    def get(self, image_name, namespace=DEFAULT_NAMESPACE):
        """Get a VirtualMachineImage"""
        return get_cr(
            group=HARVESTER_API_GROUP,
            version=HARVESTER_API_VERSION,
            namespace=namespace,
            plural=VIRTUALMACHINEIMAGE_PLURAL,
            name=image_name
        )

    def list(self, namespace=DEFAULT_NAMESPACE, label_selector=None):
        """List VirtualMachineImages"""
        result = list_cr(
            group=HARVESTER_API_GROUP,
            version=HARVESTER_API_VERSION,
            namespace=namespace,
            plural=VIRTUALMACHINEIMAGE_PLURAL,
            label_selector=label_selector
        )
        return result.get("items", [])

    def wait_for_image_created(self, image_name, namespace=DEFAULT_NAMESPACE):
        """Wait for image CR to be created"""
        for i in range(self.retry_count):
            try:
                self.get(image_name, namespace)
                logging(f"VirtualMachineImage {namespace}/{image_name} created")
                return True
            except Exception:
                logging(f"Waiting for image to be created ({i})...")
                time.sleep(self.retry_interval)
        raise AssertionError(f"VirtualMachineImage {namespace}/{image_name} not created")

    def wait_for_downloaded(self, image_name, timeout=DEFAULT_TIMEOUT):
        """
        Wait for image to be downloaded
        Check status.downloadPercent == 100 and condition Initialized=True
        """
        namespace = DEFAULT_NAMESPACE
        endtime = time.time() + timeout
        last_log_time = 0

        while time.time() < endtime:
            try:
                image = self.get(image_name, namespace)
                status = image.get("status", {})

                # 'progress' alone is not a success signal (it reaches 100 even
                # when checksum verification later fails); Imported=True is.
                download_percent = status.get("progress", 0)
                conditions = status.get("conditions", [])
                for condition in conditions:
                    is_imported = condition.get("type") == "Imported"
                    is_true = condition.get("status") == "True"
                    if is_imported and is_true:
                        logging(f"Image {namespace}/{image_name} downloaded successfully")
                        return True
                now = time.time()
                if now - last_log_time >= 30:
                    logging(f"Waiting for image download: {download_percent}%...")
                    last_log_time = now
            except Exception as e:
                logging(f"Error checking image status: {e}")
            time.sleep(self.retry_interval)
        raise AssertionError(f"Image {namespace}/{image_name} did not download within {timeout}s")

    def wait_for_ready(self, image_name, timeout=DEFAULT_TIMEOUT):
        """
        Wait for image to be ready
        Check for Ready condition
        """
        namespace = DEFAULT_NAMESPACE

        try:
            wait_for_cr_condition(
                group=HARVESTER_API_GROUP,
                version=HARVESTER_API_VERSION,
                namespace=namespace,
                plural=VIRTUALMACHINEIMAGE_PLURAL,
                name=image_name,
                condition_type="Ready",
                condition_status="True",
                timeout=timeout
            )
            logging(f"Image {namespace}/{image_name} is ready")
            return True
        except Exception as e:
            logging(f"Image did not become ready: {e}")
            raise

    def wait_for_deleted(self, image_name, timeout=DEFAULT_TIMEOUT):
        """Wait for image to be deleted"""
        namespace = DEFAULT_NAMESPACE

        wait_for_cr_deleted(
            group=HARVESTER_API_GROUP,
            version=HARVESTER_API_VERSION,
            namespace=namespace,
            plural=VIRTUALMACHINEIMAGE_PLURAL,
            name=image_name,
            timeout=timeout
        )

    def get_status(self, image_name, namespace=DEFAULT_NAMESPACE):
        """Get image status"""
        image = self.get(image_name, namespace)
        status = image.get("status", {})

        # Determine state, mirroring Harvester's own semantics
        # (pkg/image/common/operator.go):
        #  - Imported=True is the only success signal. progress is NOT one: it
        #    hits (and after a failure stays at) 100 during every download
        #    attempt even for a bad checksum, since the checksum is verified
        #    only after the download completes.
        #  - RetryLimitExceeded must be checked by STATUS, not mere presence:
        #    while retries remain the condition exists with status False, and a
        #    successful import resets it to False as well.
        conditions = status.get("conditions", [])
        progress = status.get('progress', 0)

        def cond_is_true(cond_type):
            return any(
                c.get('type') == cond_type and c.get('status') == 'True'
                for c in conditions
            )

        if cond_is_true('Imported'):
            state = IMAGE_STATE_ACTIVE
        elif cond_is_true('RetryLimitExceeded'):
            state = IMAGE_STATE_FAILED
        elif conditions or progress:
            state = IMAGE_STATE_IMPORTING
        else:
            state = "Unknown"

        return {
            'state': state,
            'download_percent': progress,
            'size': status.get('size', 0),
            'failed': status.get('failed', 0),
            'conditions': conditions
        }

    def exists(self, image_name, namespace=DEFAULT_NAMESPACE):
        """Check if image exists"""
        try:
            self.get(image_name, namespace)
            return True
        except Exception:
            return False

    def cleanup(self):
        """Clean up all test images"""
        logging('Cleaning up test images')

        try:
            # List all images with test label
            images = self.list(
                namespace=DEFAULT_NAMESPACE,
                label_selector=f"{LABEL_TEST}={LABEL_TEST_VALUE}"
            )

            for image in images:
                image_name = image['metadata']['name']
                namespace = image['metadata']['namespace']

                try:
                    logging(f'Deleting test image: {namespace}/{image_name}')
                    self.delete(image_name, namespace)
                except Exception as e:
                    logging(f'Error deleting image {image_name}: {e}', 'WARNING')
        except Exception as e:
            logging(f'Error during image cleanup: {e}', 'WARNING')
