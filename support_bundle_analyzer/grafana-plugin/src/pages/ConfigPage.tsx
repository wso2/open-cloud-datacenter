import React, { useState } from 'react';
import { Field, Input, Button, FieldSet, SecretInput } from '@grafana/ui';
import type { AppPluginMeta, PluginConfigPageProps, KeyValue } from '@grafana/data';
import { getBackendSrv } from '@grafana/runtime';
import { lastValueFrom } from 'rxjs';

interface JsonData {
  lokiDatasourceUid?: string;
  namespaceLabel?: string;
  nodeLabel?: string;
}

interface SecureJsonFields {
  grafanaToken?: string;
}

type Props = PluginConfigPageProps<AppPluginMeta<JsonData> & { secureJsonFields?: SecureJsonFields }>;

export function ConfigPage({ plugin }: Props) {
  const [lokiUid, setLokiUid] = useState(plugin.meta.jsonData?.lokiDatasourceUid ?? 'loki');
  const [namespaceLabel, setNamespaceLabel] = useState(plugin.meta.jsonData?.namespaceLabel ?? 'namespace');
  const [nodeLabel, setNodeLabel] = useState(plugin.meta.jsonData?.nodeLabel ?? 'node');
  const [grafanaToken, setGrafanaToken] = useState('');
  const [tokenConfigured, setTokenConfigured] = useState(Boolean(plugin.meta.secureJsonFields?.grafanaToken));
  const [saving, setSaving] = useState(false);
  const validLabelPattern = /^[a-zA-Z_][a-zA-Z0-9_]*$/;
  const canSave =
    lokiUid.trim().length > 0 &&
    validLabelPattern.test(namespaceLabel) &&
    (nodeLabel.length === 0 || validLabelPattern.test(nodeLabel)) &&
    (tokenConfigured || grafanaToken.trim().length > 0);

  const onSave = async () => {
    if (!canSave) {
      return;
    }

    setSaving(true);
    try {
      await lastValueFrom(
        getBackendSrv().fetch({
          url: `/api/plugins/${plugin.meta.id}/settings`,
          method: 'POST',
          data: {
            enabled: true,
            pinned: true,
            jsonData: {
              lokiDatasourceUid: lokiUid,
              namespaceLabel,
              nodeLabel,
            } as JsonData & KeyValue,
            secureJsonData: tokenConfigured ? undefined : { grafanaToken },
          },
        })
      );
      window.location.reload();
    } finally {
      setSaving(false);
    }
  };

  return (
    <FieldSet label="Harvester RCA settings">
      <Field label="Loki datasource UID" description="The Loki datasource that has your Harvester support-bundle logs">
        <Input value={lokiUid} onChange={(e) => setLokiUid(e.currentTarget.value)} width={60} />
      </Field>
      <Field
        label="Namespace label"
        description="Label name used to select namespace in Loki queries, e.g. 'namespace'"
      >
        <Input value={namespaceLabel} onChange={(e) => setNamespaceLabel(e.currentTarget.value)} width={60} />
      </Field>
      <Field
        label="Node label (optional)"
        description="Optional label used to select node-level logs in Loki, e.g. 'node'. Leave blank to keep the legacy namespace-only behavior. When set, node logs are matched alongside namespace logs."
      >
        <Input value={nodeLabel} onChange={(e) => setNodeLabel(e.currentTarget.value)} width={60} />
      </Field>
      <Field label="Grafana service account token" description="A Grafana token used by the backend to access Loki and the LLM plugin">
        <SecretInput
          value={grafanaToken}
          isConfigured={tokenConfigured}
          onChange={(e) => setGrafanaToken(e.currentTarget.value)}
          onReset={() => {
            setGrafanaToken('');
            setTokenConfigured(false);
          }}
          width={60}
        />
      </Field>
      <Button onClick={onSave} disabled={saving || !canSave}>
        {saving ? 'Saving...' : 'Save settings'}
      </Button>
    </FieldSet>
  );
}
