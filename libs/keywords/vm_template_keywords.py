"""
VM Template Keywords - creates VMTemplate() instance and delegates - NO direct API calls!
Layer 3: Keyword wrappers for Robot Framework
"""
import os
import sys

# Add the path to the utility module
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../'))) # noqa E402
from utility.utility import logging  # noqa E402
from vm_template import VMTemplate  # noqa E402
from constant import HARVESTER_PUBLIC_NAMESPACE  # noqa E402


class vm_template_keywords:
    """VM template keyword wrapper - creates VMTemplate component and delegates"""

    def __init__(self):
        """Initialize template keywords with lazy loading"""
        self._vm_template = None

    @property
    def vm_template(self):
        """Lazy initialize vm_template to allow API client setup first"""
        if self._vm_template is None:
            self._vm_template = VMTemplate()
        return self._vm_template

    def list_template_names(self, namespace=HARVESTER_PUBLIC_NAMESPACE):
        """
        List names of VM templates in a namespace

        Args:
            namespace: Namespace to list templates from

        Returns:
            list: Template names
        """
        templates = self.vm_template.list_templates(namespace)
        names = sorted(t['metadata']['name'] for t in templates)
        logging(f'Templates in {namespace}: {names}')
        return names

    def list_template_version_references(self, namespace=HARVESTER_PUBLIC_NAMESPACE):
        """
        List the template names referenced by template versions
        (spec.templateId without the namespace prefix), deduplicated.

        Args:
            namespace: Namespace to list template versions from

        Returns:
            list: Referenced template names
        """
        versions = self.vm_template.list_template_versions(namespace)
        refs = sorted({v['spec']['templateId'].split('/')[-1] for v in versions})
        logging(f'Template versions in {namespace} reference: {refs}')
        return refs

    def try_get_template(self, template_name, namespace=HARVESTER_PUBLIC_NAMESPACE):
        """
        Attempt to get a template

        Args:
            template_name: Name of the template
            namespace: Namespace of the template

        Returns:
            dict: {success, code, message}
        """
        logging(f'Trying to get template {namespace}/{template_name}')
        return self.vm_template.try_get_template(template_name, namespace)
