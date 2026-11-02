import { Alert, Code, SimpleGrid, Table } from '@mantine/core'
import { useQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { api, unwrap } from '@/api/client'
import { DefinitionList, Kpi, PageHeader, QueryState, Section, StatusBadge, Time } from '@/components/ui'
import { fmtNumber } from '@/lib/format'

export const Route = createFileRoute('/admin/system')({ component: System })

function System() {
  const info = useQuery({
    queryKey: ['system-info'],
    queryFn: async () => unwrap(await api.GET('/system/info')),
    refetchInterval: 30_000,
  })
  const dicts = useQuery({ queryKey: ['dictionaries'], queryFn: async () => unwrap(await api.GET('/dictionaries')) })
  const overview = useQuery({ queryKey: ['overview'], queryFn: async () => unwrap(await api.GET('/overview')) })
  return (
    <>
      <PageHeader
        title="System"
        description="Server version, readiness and loaded reference data. Metrics and health endpoints are served on the management listener."
      />
      <QueryState query={info}>
        {() => {
          const i = info.data
          if (!i) return null
          const build = (i.build ?? {}) as Record<string, unknown>
          return (
            <>
              {!i.ready ? (
                <Alert color="red" variant="light" mb="lg" title="Not ready">
                  {i.ready_error ?? 'The server reports it is not ready.'}
                </Alert>
              ) : null}
              <SimpleGrid cols={{ base: 2, md: 4 }} mb="lg">
                <Kpi label="Version" value={i.version} hint={`API ${i.api_version}`} />
                <Kpi
                  label="Readiness"
                  value={<StatusBadge state={i.ready ? 'ok' : 'error'} label={i.ready ? 'ready' : 'not ready'} />}
                />
                <Kpi label="Schema version" value={String(i.schema_version)} />
                <Kpi label="Started" value={<Time value={i.started_at} relative />} />
              </SimpleGrid>
              <SimpleGrid cols={{ base: 1, lg: 2 }}>
                <Section title="Build">
                  <DefinitionList
                    items={[
                      ['Installation ID', i.installation_id ? <Code key="i">{i.installation_id}</Code> : '—'],
                      ...Object.entries(build).map(
                        ([k, v]) =>
                          [k.replace(/_/g, ' '), typeof v === 'string' ? v : JSON.stringify(v)] as [string, string],
                      ),
                    ]}
                  />
                </Section>
                <Section title="Dictionaries">
                  <QueryState query={dicts}>
                    {() => (
                      <Table className="wp-table">
                        <Table.Thead>
                          <Table.Tr>
                            <Table.Th>Dictionary</Table.Th>
                            <Table.Th>Version</Table.Th>
                            <Table.Th>Entries</Table.Th>
                            <Table.Th>Origin</Table.Th>
                          </Table.Tr>
                        </Table.Thead>
                        <Table.Tbody>
                          {(dicts.data ?? []).map((d) => (
                            <Table.Tr key={d.id}>
                              <Table.Td>{d.title}</Table.Td>
                              <Table.Td>
                                {d.document_version} ({d.document_date})
                              </Table.Td>
                              <Table.Td className="wp-num">{fmtNumber(d.entry_count)}</Table.Td>
                              <Table.Td>{d.origin}</Table.Td>
                            </Table.Tr>
                          ))}
                        </Table.Tbody>
                      </Table>
                    )}
                  </QueryState>
                </Section>
                <Section title="Activity (24 h)">
                  <QueryState query={overview}>
                    {() => (
                      <DefinitionList
                        items={[
                          ['Vehicles', fmtNumber(overview.data?.vehicles)],
                          ['Observations accepted', fmtNumber(overview.data?.observations_24h)],
                          ['Events', fmtNumber(overview.data?.events_24h)],
                          [
                            'Imports',
                            Object.entries(overview.data?.imports_24h_by_state ?? {})
                              .map(([s, n]) => `${String(n)} ${s}`)
                              .join(' · ') || '—',
                          ],
                          [
                            'Sources',
                            Object.entries(overview.data?.sources_by_state ?? {})
                              .map(([s, n]) => `${String(n)} ${s.replace(/_/g, ' ')}`)
                              .join(' · ') || '—',
                          ],
                          [
                            'Adapters',
                            Object.entries(overview.data?.adapters_by_state ?? {})
                              .map(([s, n]) => `${String(n)} ${s}`)
                              .join(' · ') || '—',
                          ],
                        ]}
                      />
                    )}
                  </QueryState>
                </Section>
              </SimpleGrid>
            </>
          )
        }}
      </QueryState>
    </>
  )
}
