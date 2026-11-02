import {
  Badge,
  Card,
  Group,
  Progress,
  SimpleGrid,
  Stack,
  Text,
  ThemeIcon,
  Tooltip,
  UnstyledButton,
} from '@mantine/core'
import {
  IconAlertTriangle,
  IconCircleCheck,
  IconDroplet,
  IconGasStation,
  IconGauge,
  IconLock,
  IconLockOpen,
  IconRoad,
  IconTemperature,
  IconTool,
  IconBattery2,
} from '@tabler/icons-react'
import type { ReactNode } from 'react'
import type { StateValue } from '@/api/client'
import { fmtRelative } from '@/lib/format'
import { presentValue, signalGroup, signalLabel } from '@/lib/signals'
import { SeriesChart, useRangeEnd } from './SeriesChart'
import { EmptyBlock, Section, Time } from './ui'

/**
 * Vehicle overview: headline figures, door/lock/window status, service and tyres, short trends, and every signal
 * grouped by its dictionary cluster. Headline cards use the dictionary names of the continuous data list (flat layout);
 * vehicles that report other names still get the grouped view.
 */
export function VehicleOverview({
  vehicleId,
  values,
  onPick,
}: {
  vehicleId: string
  values: StateValue[]
  onPick: (key: string) => void
}) {
  if (values.length === 0) {
    return (
      <Card>
        <EmptyBlock
          title="No data yet"
          description="Import a delivery or let a source collect one to see this vehicle's state."
        />
      </Card>
    )
  }
  const byName = new Map(values.map((v) => [v.signal.name, v]))
  const get = (...names: string[]) => names.map((n) => byName.get(n)).find(Boolean)
  const lastReport = values
    .filter((v) => v.time_basis === 'observed')
    .reduce<string | undefined>((m, v) => (!m || v.observed_at > m ? v.observed_at : m), undefined)

  const headline: { icon: ReactNode; v?: StateValue; progress?: boolean }[] = [
    { icon: <IconRoad size={18} />, v: get('cruising_range_combined', 'cruising_range_primary_engine') },
    { icon: <IconGasStation size={18} />, v: get('fuel_level_current_level'), progress: true },
    { icon: <IconBattery2 size={18} />, v: get('hv_soc', 'battery_charging_status_soc'), progress: true },
    { icon: <IconGauge size={18} />, v: get('mileage') },
    { icon: <IconTemperature size={18} />, v: get('outside_temperature') },
    { icon: <IconBattery2 size={18} />, v: get('bem_level'), progress: true },
  ]
  const cards = headline.filter((h): h is { icon: ReactNode; v: StateValue; progress?: boolean } => h.v !== undefined)

  return (
    <Stack>
      {lastReport ? (
        <Text size="sm" c="dimmed">
          The car last reported <Time value={lastReport} relative />. Values keep the time the car captured them; a
          parked car sends nothing new.
        </Text>
      ) : null}
      {cards.length > 0 ? (
        <SimpleGrid cols={{ base: 2, sm: 3, lg: 6 }}>
          {cards.map(({ icon, v, progress }) => (
            <HeadlineCard
              key={v.signal.key}
              icon={icon}
              v={v}
              progress={progress}
              onClick={() => {
                onPick(v.signal.key)
              }}
            />
          ))}
        </SimpleGrid>
      ) : null}
      <SimpleGrid cols={{ base: 1, lg: 3 }}>
        <AccessCard values={values} />
        <ServiceCard get={get} onPick={onPick} />
        <TyresCard values={values} onPick={onPick} />
      </SimpleGrid>
      <Trends
        vehicleId={vehicleId}
        candidates={[get('fuel_level_current_level', 'hv_soc'), get('cruising_range_combined'), get('mileage')]}
      />
      <GroupedSignals values={values} onPick={onPick} />
    </Stack>
  )
}

function HeadlineCard({
  icon,
  v,
  progress,
  onClick,
}: {
  icon: ReactNode
  v: StateValue
  progress?: boolean
  onClick: () => void
}) {
  const p = presentValue(v.signal, v)
  return (
    <Card
      padding="md"
      component="button"
      type="button"
      onClick={onClick}
      style={{ cursor: 'pointer', textAlign: 'left' }}
      data-testid="headline-card"
    >
      <Group gap={8} wrap="nowrap" mb={6}>
        <ThemeIcon variant="light" size={28} radius="sm">
          {icon}
        </ThemeIcon>
        <Text size="xs" c="dimmed" tt="uppercase" fw={600} lts="0.03em" lineClamp={1}>
          {signalLabel(v.signal)}
        </Text>
      </Group>
      <Text className="wp-kpi-value" c={p.muted ? 'dimmed' : undefined}>
        {p.text}
      </Text>
      {progress && v.num !== undefined && v.quality === 'ok' ? (
        <Progress value={Math.max(0, Math.min(100, v.num))} size="sm" mt={8} aria-label={signalLabel(v.signal)} />
      ) : null}
      <Text size="xs" c="dimmed" mt={6}>
        {fmtRelative(v.observed_at)}
      </Text>
    </Card>
  )
}

/** Summarises a family of state signals ("…_door lock") as "all X" or the parts that are not. */
function summarise(values: StateValue[], match: RegExp, good: string[]) {
  const items = values.filter((v) => match.test(v.signal.name) && v.quality === 'ok')
  const known = items
    .map((v) => ({ v, p: presentValue(v.signal, v) }))
    .filter((x) => x.p.state && x.p.state !== 'unsupported' && x.p.state !== 'invalid')
  const bad = known.filter((x) => !good.includes(x.p.state ?? ''))
  return { known, bad }
}

function StatusRow({ ok, label, detail, icon }: { ok: boolean; label: string; detail?: ReactNode; icon?: ReactNode }) {
  return (
    <Group justify="space-between" wrap="nowrap" py={6} style={{ borderTop: '1px solid var(--wp-border)' }}>
      <Group gap={8} wrap="nowrap">
        {icon ??
          (ok ? (
            <IconCircleCheck size={18} color="var(--wp-success)" />
          ) : (
            <IconAlertTriangle size={18} color="var(--wp-warning)" />
          ))}
        <Text size="sm">{label}</Text>
      </Group>
      <Text size="sm" c={ok ? 'dimmed' : 'var(--wp-warning)'} ta="right">
        {detail}
      </Text>
    </Group>
  )
}

function AccessCard({ values }: { values: StateValue[] }) {
  const locks = summarise(values, /^locked_state_/, ['locked'])
  const doors = summarise(values, /^open_state_/, ['closed'])
  const windows = summarise(
    values,
    /window_lifter$|^state_sunroof|^state_of_hood|^state_service_hatch|^state_spoiler/,
    ['closed'],
  )
  const brake = values.find((v) => v.signal.name === 'parking_brake')
  const lights = values.find((v) => v.signal.name === 'parking_lights')
  const rows = [
    locks.known.length > 0 ? (
      <StatusRow
        key="locks"
        ok={locks.bad.length === 0}
        icon={
          locks.bad.length === 0 ? (
            <IconLock size={18} color="var(--wp-success)" />
          ) : (
            <IconLockOpen size={18} color="var(--wp-warning)" />
          )
        }
        label="Locks"
        detail={
          locks.bad.length === 0
            ? 'All locked'
            : `Unlocked: ${locks.bad.map((x) => signalLabel(x.v.signal).replace(/ lock$/, '')).join(', ')}`
        }
      />
    ) : null,
    doors.known.length > 0 ? (
      <StatusRow
        key="doors"
        ok={doors.bad.length === 0}
        label="Doors, bonnet, tailgate"
        detail={
          doors.bad.length === 0
            ? 'All closed'
            : `Open: ${doors.bad.map((x) => signalLabel(x.v.signal).replace(/ open$/, '')).join(', ')}`
        }
      />
    ) : null,
    windows.known.length > 0 ? (
      <StatusRow
        key="windows"
        ok={windows.bad.length === 0}
        label="Windows and roof"
        detail={
          windows.bad.length === 0
            ? 'All closed'
            : `Open: ${windows.bad.map((x) => signalLabel(x.v.signal)).join(', ')}`
        }
      />
    ) : null,
    brake ? <StatusRow key="brake" ok label="Parking brake" detail={presentValue(brake.signal, brake).text} /> : null,
    lights ? (
      <StatusRow key="lights" ok label="Parking lights" detail={presentValue(lights.signal, lights).text} />
    ) : null,
  ].filter(Boolean)
  if (rows.length === 0) return null
  const first = values.find((v) => v.signal.name.startsWith('locked_state_'))
  return (
    <Section
      title="Doors and locks"
      actions={
        first ? (
          <Text size="xs" c="dimmed">
            {fmtRelative(first.observed_at)}
          </Text>
        ) : null
      }
    >
      <Stack gap={0} px="lg" pb="md">
        {rows}
      </Stack>
    </Section>
  )
}

function ServiceCard({
  get,
  onPick,
}: {
  get: (...n: string[]) => StateValue | undefined
  onPick: (key: string) => void
}) {
  const items: [string, StateValue | undefined, StateValue | undefined][] = [
    [
      'Inspection',
      get('maintenance_interval__time_until_inspection'),
      get('maintenance_interval_distance_until_inspection'),
    ],
    [
      'Oil change',
      get('maintenance_interval__time_until_oil_change'),
      get('maintenance_interval_distance_until_oil_change'),
    ],
  ]
  const oil = get('oil_level_actual_level')
  const adblue = get('scr_range')
  if (!items.some(([, a, b]) => a ?? b) && !oil && !adblue) return null
  return (
    <Section title="Service">
      <Stack gap={0} px="lg" pb="md">
        {items.map(([label, time, dist]) => {
          if (!time && !dist) return null
          const t = time ? presentValue(time.signal, time) : undefined
          const d = dist ? presentValue(dist.signal, dist) : undefined
          const overdue = t?.overdue === true || d?.overdue === true
          const target = time ?? dist
          const text = [t?.text, d?.text].filter(Boolean).join(' · ')
          return (
            <StatusRow
              key={label}
              ok={!overdue}
              icon={<IconTool size={18} color={overdue ? 'var(--wp-danger)' : 'var(--wp-success)'} />}
              label={label}
              detail={
                <UnstyledButton
                  onClick={() => {
                    if (target) onPick(target.signal.key)
                  }}
                  fz="sm"
                  c={overdue ? 'var(--wp-danger)' : 'dimmed'}
                >
                  {overdue ? text : `in ${text}`}
                </UnstyledButton>
              }
            />
          )
        })}
        {oil ? (
          <StatusRow
            ok
            icon={<IconDroplet size={18} color="var(--wp-info)" />}
            label="Oil level"
            detail={presentValue(oil.signal, oil).text}
          />
        ) : null}
        {adblue ? (
          <StatusRow
            ok
            icon={<IconDroplet size={18} color="var(--wp-info)" />}
            label="AdBlue range"
            detail={presentValue(adblue.signal, adblue).text}
          />
        ) : null}
      </Stack>
    </Section>
  )
}

const wheels: [string, string][] = [
  ['front_left', 'Front left'],
  ['front_right', 'Front right'],
  ['rear_left', 'Rear left'],
  ['rear_right', 'Rear right'],
]

function TyresCard({ values, onPick }: { values: StateValue[]; onPick: (key: string) => void }) {
  const by = (kind: string, wheel: string) => values.find((v) => v.signal.name === `tyre_pressure_${kind}_${wheel}`)
  const rows = wheels
    .map(([w, label]) => ({ label, actual: by('actual', w), required: by('required', w) }))
    .filter((r) => r.actual)
  if (rows.length === 0) return null
  return (
    <Section
      title="Tyres"
      description={
        <Tooltip
          label="The dictionary gives the pressure unit as “10kPA / Bar / PSI / kPA”; values are shown as reported."
          multiline
          w={260}
        >
          <span>Actual / required pressure (unit as reported)</span>
        </Tooltip>
      }
    >
      <Stack gap={0} px="lg" pb="md">
        {rows.map((r) => {
          const a = r.actual ? presentValue({ ...r.actual.signal, unit: '' }, { ...r.actual, unit: '' }) : undefined
          const q = r.required
            ? presentValue({ ...r.required.signal, unit: '' }, { ...r.required, unit: '' })
            : undefined
          const low =
            r.actual?.num != null &&
            r.required?.num != null &&
            r.required.num > 0 &&
            r.actual.num < r.required.num * 0.9
          return (
            <StatusRow
              key={r.label}
              ok={!low}
              label={r.label}
              detail={
                <UnstyledButton
                  onClick={() => {
                    if (r.actual) onPick(r.actual.signal.key)
                  }}
                  fz="sm"
                  c="dimmed"
                >
                  {a?.text ?? '—'} / {q?.text ?? '—'}
                </UnstyledButton>
              }
            />
          )
        })}
      </Stack>
    </Section>
  )
}

function Trends({ vehicleId, candidates }: { vehicleId: string; candidates: (StateValue | undefined)[] }) {
  const to = useRangeEnd()
  const from = new Date(to.getTime() - 30 * 86400_000)
  const shown = candidates.filter((c): c is StateValue => Boolean(c))
  if (shown.length === 0) return null
  return (
    <SimpleGrid cols={{ base: 1, lg: shown.length }}>
      {shown.map((v) => (
        <Section key={v.signal.key} title={`${signalLabel(v.signal)} · 30 days`} padded>
          <SeriesChart
            vehicle={vehicleId}
            signal={v.signal.key}
            name={signalLabel(v.signal)}
            from={from}
            to={to}
            height={180}
            compact
          />
        </Section>
      ))}
    </SimpleGrid>
  )
}

function GroupedSignals({ values, onPick }: { values: StateValue[]; onPick: (key: string) => void }) {
  const groups = new Map<string, StateValue[]>()
  for (const v of values) {
    const g = signalGroup(v.signal)
    groups.set(g, [...(groups.get(g) ?? []), v])
  }
  const sorted = [...groups.entries()].sort(([a], [b]) => a.localeCompare(b))
  return (
    <SimpleGrid cols={{ base: 1, md: 2, xl: 3 }}>
      {sorted.map(([group, vs]) => (
        <Section
          key={group}
          title={group}
          actions={
            <Badge variant="light" color="gray">
              {vs.length}
            </Badge>
          }
        >
          <Stack gap={0} px="lg" pb="md">
            {vs
              .slice()
              .sort((a, b) => signalLabel(a.signal).localeCompare(signalLabel(b.signal)))
              .map((v) => {
                const p = presentValue(v.signal, v)
                return (
                  <UnstyledButton
                    key={v.signal.key}
                    onClick={() => {
                      onPick(v.signal.key)
                    }}
                    py={5}
                    style={{ borderTop: '1px solid var(--wp-border)' }}
                  >
                    <Group justify="space-between" wrap="nowrap">
                      <Text size="sm" lineClamp={1}>
                        {signalLabel(v.signal)}
                      </Text>
                      <Text
                        size="sm"
                        fw={550}
                        c={p.muted ? 'dimmed' : p.overdue ? 'var(--wp-danger)' : undefined}
                        ta="right"
                        style={{ whiteSpace: 'nowrap' }}
                      >
                        {p.text}
                      </Text>
                    </Group>
                  </UnstyledButton>
                )
              })}
          </Stack>
        </Section>
      ))}
    </SimpleGrid>
  )
}
