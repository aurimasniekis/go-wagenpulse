import { Alert, Badge, Button, Group, Select, Stack, Table, Text } from '@mantine/core'
import { IconPlayerPlay } from '@tabler/icons-react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api, unwrap, type Definition } from '@/api/client'
import { fmtNumber, fmtValue } from '@/lib/format'
import { ranges } from './SeriesChart'
import { EmptyBlock, ErrorBlock, ScrollRegion, Time } from './ui'

/** Dry-runs a definition over stored history for one vehicle. Nothing is written or delivered. */
export function RulePreview({ definition }: { definition: Definition }) {
  const vehicles = useQuery({
    queryKey: ['vehicles', 'list', false],
    queryFn: async () => unwrap(await api.GET('/vehicles', { params: { query: { limit: 200 } } })),
  })
  const [vehicle, setVehicle] = useState<string | null>(null)
  const [range, setRange] = useState('30d')
  const v = vehicle ?? (definition.vehicle_ids[0] || vehicles.data?.items[0]?.id) ?? null
  const preview = useMutation({
    mutationFn: async () => {
      const to = new Date()
      const from = new Date(to.getTime() - (ranges[range]?.ms ?? 0))
      return unwrap(
        await api.POST('/rules/preview', {
          body: { definition, vehicle_id: v ?? '', from: from.toISOString(), to: to.toISOString() },
        }),
      )
    },
  })
  const r = preview.data
  return (
    <Stack gap="sm">
      <Text size="sm" c="dimmed">
        Runs the rule over stored observations as if they arrived in order. Nothing is stored or delivered.
      </Text>
      <Group align="flex-end" wrap="wrap">
        <Select
          label="Vehicle"
          data={(vehicles.data?.items ?? [])
            .filter((x) => definition.vehicle_ids.length === 0 || definition.vehicle_ids.includes(x.id))
            .map((x) => ({ value: x.id, label: x.display_name }))}
          value={v}
          onChange={setVehicle}
          w={220}
          allowDeselect={false}
        />
        <Select
          label="History"
          data={Object.entries(ranges).map(([value, x]) => ({ value, label: `Last ${x.label}` }))}
          value={range}
          onChange={(x) => {
            setRange(x ?? '30d')
          }}
          w={140}
          allowDeselect={false}
        />
        <Button
          leftSection={<IconPlayerPlay size={16} />}
          variant="default"
          onClick={() => {
            preview.mutate()
          }}
          loading={preview.isPending}
          disabled={!v}
        >
          Run preview
        </Button>
      </Group>
      {preview.isError ? <ErrorBlock error={preview.error} title="Preview failed" /> : null}
      {r ? (
        <>
          <Group gap="xs">
            <Badge variant="light">{fmtNumber(r.observations_evaluated)} observations evaluated</Badge>
            <Badge variant="light" color={r.events.length ? 'blue' : 'gray'}>
              {r.events.length} event(s)
            </Badge>
            {r.suppressed_by_cooldown ? (
              <Badge variant="light" color="gray">
                {r.suppressed_by_cooldown} suppressed by cooldown
              </Badge>
            ) : null}
          </Group>
          {r.truncated ? (
            <Alert variant="light" color="yellow">
              The preview stopped at its limit; the rule would fire more often than shown.
            </Alert>
          ) : null}
          {r.events.length === 0 ? (
            <EmptyBlock
              title="The rule would not have fired"
              description="Adjust the threshold or pick a longer history."
            />
          ) : (
            <ScrollRegion label="Preview events" maxHeight={360}>
              <Table className="wp-table" stickyHeader>
                <Table.Thead>
                  <Table.Tr>
                    <Table.Th>Observed</Table.Th>
                    <Table.Th>Event</Table.Th>
                    <Table.Th>Previous</Table.Th>
                    <Table.Th>Value</Table.Th>
                  </Table.Tr>
                </Table.Thead>
                <Table.Tbody>
                  {r.events.map((e, i) => (
                    <Table.Tr key={`${e.observed_at}-${String(i)}`}>
                      <Table.Td>
                        <Time value={e.observed_at} />
                      </Table.Td>
                      <Table.Td>{e.type}</Table.Td>
                      <Table.Td className="wp-num">{e.previous ? fmtValue(e.previous) : '—'}</Table.Td>
                      <Table.Td className="wp-num">{e.value ? fmtValue(e.value) : '—'}</Table.Td>
                    </Table.Tr>
                  ))}
                </Table.Tbody>
              </Table>
            </ScrollRegion>
          )}
        </>
      ) : null}
    </Stack>
  )
}
