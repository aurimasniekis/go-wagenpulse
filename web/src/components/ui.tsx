import {
  Box,
  Alert,
  Badge,
  Button,
  Card,
  Center,
  CopyButton,
  Group,
  Loader,
  Paper,
  Stack,
  Text,
  ThemeIcon,
  Title,
  Tooltip,
  type MantineColor,
} from '@mantine/core'
import {
  IconAlertTriangle,
  IconCheck,
  IconCircleDashed,
  IconCloudOff,
  IconCopy,
  IconInbox,
  IconRefresh,
} from '@tabler/icons-react'
import type { ReactNode } from 'react'
import { errorMessage } from '@/lib/notify'
import { fmtDateTime, fmtISO, fmtRelative } from '@/lib/format'
import { ApiError } from '@/api/client'

/** Page title row with optional description and actions. */
export function PageHeader({
  title,
  description,
  actions,
}: {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
}) {
  return (
    <Group justify="space-between" align="flex-start" mb="lg" wrap="wrap" gap="md">
      <Stack gap={4} style={{ minWidth: 0 }}>
        <Title order={1}>{title}</Title>
        {description ? (
          <Text c="dimmed" size="sm" maw={760}>
            {description}
          </Text>
        ) : null}
      </Stack>
      {actions ? <Group gap="sm">{actions}</Group> : null}
    </Group>
  )
}

export function LoadingBlock({ label = 'Loading…', h = 160 }: { label?: string; h?: number }) {
  return (
    <Center h={h} role="status" aria-live="polite">
      <Group gap="sm">
        <Loader size="sm" type="dots" />
        <Text c="dimmed" size="sm">
          {label}
        </Text>
      </Group>
    </Center>
  )
}

/** Error state with a retry button. Network failures are shown as "disconnected". */
export function ErrorBlock({ error, onRetry, title }: { error: unknown; onRetry?: () => void; title?: string }) {
  const disconnected =
    error instanceof TypeError || (error instanceof ApiError && error.status >= 502 && error.status <= 504)
  return (
    <Alert
      color={disconnected ? 'orange' : 'red'}
      variant="light"
      icon={disconnected ? <IconCloudOff size={18} /> : <IconAlertTriangle size={18} />}
      title={title ?? (disconnected ? 'Cannot reach WagenPulse' : 'Could not load this data')}
      role="alert"
    >
      <Stack gap="xs" align="flex-start">
        <Text size="sm">
          {disconnected ? 'The server did not answer. It may be restarting; your data is safe.' : errorMessage(error)}
        </Text>
        {onRetry ? (
          <Button size="xs" variant="default" leftSection={<IconRefresh size={14} />} onClick={onRetry}>
            Retry
          </Button>
        ) : null}
      </Stack>
    </Alert>
  )
}

export function EmptyBlock({
  title,
  description,
  action,
  icon,
}: {
  title: string
  description?: ReactNode
  action?: ReactNode
  icon?: ReactNode
}) {
  return (
    <Center py={48} px="md">
      <Stack align="center" gap="xs" maw={480} ta="center">
        <ThemeIcon variant="light" size={44} radius="md" color="gray">
          {icon ?? <IconInbox size={24} />}
        </ThemeIcon>
        <Text fw={600}>{title}</Text>
        {description ? (
          <Text c="dimmed" size="sm">
            {description}
          </Text>
        ) : null}
        {action}
      </Stack>
    </Center>
  )
}

/** Renders a query's loading/error state, or its children. */
export function QueryState({
  query,
  children,
  loadingLabel,
}: {
  query: { isPending: boolean; isError: boolean; error: unknown; refetch: () => unknown }
  children: () => ReactNode
  loadingLabel?: string
}) {
  if (query.isPending) return <LoadingBlock label={loadingLabel} />
  if (query.isError) return <ErrorBlock error={query.error} onRetry={() => void query.refetch()} />
  return <>{children()}</>
}

const stateColors: Record<string, MantineColor> = {
  // generic
  ok: 'green',
  active: 'green',
  enabled: 'green',
  succeeded: 'green',
  connected: 'green',
  authorized: 'green',
  processed: 'green',
  fresh: 'green',
  // in progress
  queued: 'blue',
  running: 'blue',
  pending: 'blue',
  processing: 'blue',
  connecting: 'blue',
  configured: 'blue',
  stored: 'blue',
  // attention
  partial: 'yellow',
  retrying: 'yellow',
  rate_limited: 'yellow',
  stale: 'yellow',
  no_active_request: 'yellow',
  unrecognized: 'yellow',
  disconnected: 'orange',
  unavailable: 'orange',
  expired: 'gray',
  // failure
  failed: 'red',
  dead: 'red',
  error: 'red',
  rejected: 'red',
  revoked: 'gray',
  reauthorization_required: 'red',
  suspended: 'gray',
  // neutral
  cancelled: 'gray',
  disabled: 'gray',
  archived: 'gray',
  unsupported: 'gray',
}

const stateLabels: Record<string, string> = {
  no_active_request: 'No active request',
  reauthorization_required: 'Reauthorization required',
  rate_limited: 'Rate limited',
  succeeded: 'Succeeded',
}

/** A coloured badge for a lifecycle state. */
export function StatusBadge({ state, label }: { state: string; label?: string }) {
  const color = stateColors[state] ?? 'gray'
  const text = label ?? stateLabels[state] ?? state.replace(/_/g, ' ')
  return (
    <Badge
      color={color}
      variant="light"
      leftSection={<IconCircleDashed size={10} style={{ display: 'none' }} />}
      tt="none"
    >
      {text}
    </Badge>
  )
}

/** A timestamp in the display zone, with the exact UTC instant in a tooltip. */
export function Time({
  value,
  relative = false,
  seconds = true,
}: {
  value: string | null | undefined
  relative?: boolean
  seconds?: boolean
}) {
  if (!value)
    return (
      <Text span c="dimmed">
        —
      </Text>
    )
  return (
    <Tooltip label={`${fmtDateTime(value)} · ${fmtISO(value)}`}>
      <Text span className="wp-num" style={{ whiteSpace: 'nowrap' }} size="inherit">
        <time dateTime={fmtISO(value)}>{relative ? fmtRelative(value) : fmtDateTime(value, seconds)}</time>
      </Text>
    </Tooltip>
  )
}

/** A KPI tile. */
export function Kpi({
  label,
  value,
  hint,
  color,
}: {
  label: string
  value: ReactNode
  hint?: ReactNode
  color?: string
}) {
  return (
    <Card padding="md">
      <Text size="xs" c="dimmed" tt="uppercase" fw={600} lts="0.04em">
        {label}
      </Text>
      <Text className="wp-kpi-value" mt={6} c={color}>
        {value}
      </Text>
      {hint ? (
        <Text size="xs" c="dimmed" mt={4}>
          {hint}
        </Text>
      ) : null}
    </Card>
  )
}

/** A titled card section. */
export function Section({
  title,
  description,
  actions,
  children,
  p = 'lg',
  padded = false,
}: {
  title: ReactNode
  description?: ReactNode
  actions?: ReactNode
  children: ReactNode
  p?: string
  /** Pads the body (forms, free content); tables and lists render edge to edge. */
  padded?: boolean
}) {
  return (
    <Card padding={0}>
      <Group justify="space-between" px={p} pt="md" pb={description ? 4 : 'sm'} wrap="wrap" gap="sm">
        <Title order={2} size="h4">
          {title}
        </Title>
        {actions ? <Group gap="xs">{actions}</Group> : null}
      </Group>
      {description ? (
        <Text size="sm" c="dimmed" px={p} pb="sm">
          {description}
        </Text>
      ) : null}
      {padded ? (
        <Box px={p} pb={p}>
          {children}
        </Box>
      ) : (
        children
      )}
    </Card>
  )
}

/** A one-time secret display with copy action. */
export function SecretOnce({ value, label, warning }: { value: string; label: string; warning: ReactNode }) {
  return (
    <Stack gap="sm">
      <Alert color="yellow" variant="light" icon={<IconAlertTriangle size={18} />}>
        {warning}
      </Alert>
      <Text size="sm" fw={600}>
        {label}
      </Text>
      <Paper withBorder p="sm" bg="var(--wp-surface2)">
        <Group justify="space-between" wrap="nowrap" gap="sm">
          <Text className="wp-mono" style={{ wordBreak: 'break-all' }} data-testid="secret-value">
            {value}
          </Text>
          <CopyButton value={value}>
            {({ copied, copy }) => (
              <Button
                size="xs"
                variant={copied ? 'light' : 'filled'}
                color={copied ? 'green' : undefined}
                leftSection={copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
                onClick={copy}
              >
                {copied ? 'Copied' : 'Copy'}
              </Button>
            )}
          </CopyButton>
        </Group>
      </Paper>
    </Stack>
  )
}

/** Key/value rows for detail views. */
export function DefinitionList({ items }: { items: [ReactNode, ReactNode][] }) {
  return (
    <Stack gap={0} px="lg" pb="sm">
      {items.map(([k, v], i) => (
        <Group
          key={i}
          py={8}
          gap="md"
          wrap="nowrap"
          align="flex-start"
          style={{ borderBottom: '1px solid var(--wp-border)' }}
        >
          <Text size="sm" c="dimmed" w={190} style={{ flexShrink: 0 }}>
            {k}
          </Text>
          <div style={{ minWidth: 0, flex: 1, fontSize: 'var(--mantine-font-size-sm)' }}>{v}</div>
        </Group>
      ))}
    </Stack>
  )
}

/**
 * A scrollable container that keyboard users can reach (focusable, labelled region): wide tables and long lists
 * scroll inside it without trapping content out of reach (WCAG 2.1.1).
 */
export function ScrollRegion({
  label,
  children,
  maxHeight,
}: {
  label: string
  children: ReactNode
  maxHeight?: number
}) {
  return (
    <div
      role="region"
      aria-label={label}
      // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex -- scrollable regions must be keyboard-focusable (axe)
      tabIndex={0}
      className="wp-scroll-region"
      style={{ overflow: 'auto', maxHeight }}
    >
      {children}
    </div>
  )
}
