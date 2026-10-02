""" Setting Keyword Wrapper

Layer 3: Keyword wrapper (NO direct API calls)
"""

import os
import sys

# Add the path to the utility module
sys.path.insert(0, os.path.abspath(os.path.join(os.path.dirname(__file__), '../'))) # noqa E402
from setting import Setting  # noqa E402
from utility.utility import logging  # noqa E402


class setting_keywords:
    """Setting keyword wrapper - creates Setting component and delegates"""

    def __init__(self):
        """Initialize setting keywords with lazy loading"""
        self._setting = None

    @property
    def setting(self):
        """Lazy initialize setting to allow API client setup first"""
        if self._setting is None:
            self._setting = Setting()
        return self._setting

    def get(self, setting_id):
        return self.setting.get(setting_id)

    def get_value(self, setting_id):
        return self.setting.get(setting_id).get('value')

    def get_condition_message(self, setting_id: str, condition_type: str):
        conditions = self.setting.get(setting_id).get('status', {}).get('conditions', [])
        for condition in conditions:
            if condition.get('type') == condition_type:
                return condition.get('message')
        return None

    def enable(self, setting_id):
        setting = self.setting.enable(setting_id)
        return setting

    def update(self, setting_id, value):
        setting = self.setting.update(setting_id, value)
        return setting
