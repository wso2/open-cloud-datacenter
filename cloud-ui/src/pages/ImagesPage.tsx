import {
  Body1,
  Card,
  Subtitle1,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Title2,
  makeStyles,
  tokens,
} from '@fluentui/react-components';
import { ArrowClockwise20Regular, Image24Regular } from '@fluentui/react-icons';
import { useQuery } from '@tanstack/react-query';
import { Button } from '@fluentui/react-components';
import { useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { EmptyState, ErrorState, LoadingState } from '../components/list/PageStates';
import { useListPageStyles } from '../components/list/useListPageStyles';
import StatusPill from '../components/StatusPill';

/**
 * Images is intentionally a read-only catalog. Operators (cloud admins)
 * curate the image set out of band — via dcctl, TF, or direct calls
 * to POST /v1/images. cloud-ui never writes to /v1/images. Users
 * picking an image during VM/cluster creation see only the curated
 * list; the underlying Harvester namespace is hidden.
 *
 * No upload, no delete, no kebab actions. If a tenant needs a new
 * image, that's a request to the cloud team, not a self-serve
 * operation.
 */

const usePageStyles = makeStyles({
  notice: {
    backgroundColor: tokens.colorNeutralBackground2,
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase200,
    border: `1px dashed ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
    padding: tokens.spacingHorizontalL,
  },
});

interface Image {
  id: string;
  display_name: string;
  namespace: string;
}

export default function ImagesPage() {
  const styles = useListPageStyles();
  const pageStyles = usePageStyles();
  const api = useApi();
  const { tenantId } = useParams<{ tenantId: string }>();

  const imagesQuery = useQuery({
    queryKey: ['images', tenantId],
    enabled: Boolean(tenantId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/images', {
        params: { path: { tenant_id: tenantId! } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as Image[];
    },
  });

  const images = imagesQuery.data ?? [];
  const count = images.length;

  return (
    <div className={styles.root}>
      <div className={styles.header}>
        <Title2>Images</Title2>
        <Subtitle1 className={styles.subtitle}>
          {imagesQuery.isLoading
            ? 'Loading…'
            : `${count} image${count === 1 ? '' : 's'} available for VM and cluster creation`}
        </Subtitle1>
      </div>

      <div className={styles.cmdBar}>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => imagesQuery.refetch()}
          disabled={imagesQuery.isFetching}
        >
          Refresh
        </Button>
      </div>

      <div className={pageStyles.notice}>
        Images are curated by the WSO2 Infrastructure Platform team. To request a new image, contact
        the cloud operations team — uploads aren&apos;t self-service.
      </div>

      {imagesQuery.isLoading && <LoadingState label="Loading images…" />}

      {imagesQuery.isError && !imagesQuery.isLoading && (
        <ErrorState message={`Failed to load images: ${(imagesQuery.error as Error).message}`} />
      )}

      {!imagesQuery.isLoading && !imagesQuery.isError && count === 0 && (
        <EmptyState
          icon={<Image24Regular />}
          title="No images yet"
          description="The cloud team hasn't published any images for this datacenter yet. Contact cloud operations to get started."
        />
      )}

      {!imagesQuery.isLoading && !imagesQuery.isError && count > 0 && (
        <Card className={styles.tableCard}>
          <Table size="small" aria-label="Images">
            <TableHeader>
              <TableRow>
                <TableHeaderCell>Name</TableHeaderCell>
                <TableHeaderCell>Status</TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {images.map((img) => (
                <TableRow key={img.id}>
                  <TableCell>
                    <Body1 style={{ fontWeight: 600 }}>{img.display_name}</Body1>
                  </TableCell>
                  <TableCell>
                    <StatusPill status="AVAILABLE" />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </div>
  );
}
