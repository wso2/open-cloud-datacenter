import { Tooltip } from '@fluentui/react-components';
import type { ReactElement } from 'react';

/**
 * PermissionTooltip wraps a control in a Fluent Tooltip only when `when` is true.
 *
 * Use it to explain why a `disabledFocusable` button is greyed out — e.g. the
 * caller lacks the role to perform the action. Fluent tooltips show on a
 * `disabledFocusable` button (it stays focusable), whereas a native `title`
 * attribute does not. When `when` is false the child renders unchanged, with no
 * tooltip and no extra DOM.
 *
 *   <PermissionTooltip when={!canWrite} reason="You need write access on this tenant to create VNets">
 *     <Button disabledFocusable={!canWrite} ...>Create VNet</Button>
 *   </PermissionTooltip>
 */
export function PermissionTooltip({
  when,
  reason,
  children,
}: {
  when: boolean;
  reason: string;
  children: ReactElement;
}) {
  if (!when) return children;
  return (
    <Tooltip content={reason} relationship="description" withArrow>
      {children}
    </Tooltip>
  );
}
