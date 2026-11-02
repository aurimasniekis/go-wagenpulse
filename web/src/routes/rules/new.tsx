import { Grid } from '@mantine/core'
import { useQueryClient } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { api, idempotencyKey, unwrap, type Definition } from '@/api/client'
import { cleanDefinition, emptyDefinition, RuleForm } from '@/components/RuleForm'
import { RulePreview } from '@/components/RulePreview'
import { PageHeader, Section } from '@/components/ui'
import { notifySuccess } from '@/lib/notify'

export const Route = createFileRoute('/rules/new')({ component: NewRule })

function NewRule() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [def, setDef] = useState<Definition>(cleanDefinition(emptyDefinition()))
  return (
    <>
      <PageHeader title="New rule" description="Preview the rule against stored history before enabling it." />
      <Grid>
        <Grid.Col span={{ base: 12, lg: 7 }}>
          <Section title="Definition" padded>
            <RuleForm
              submitLabel="Create rule"
              onDefinitionChange={setDef}
              onCancel={() => void navigate({ to: '/rules' })}
              onSubmit={async (body) => {
                const r = unwrap(await api.POST('/rules', { body, headers: idempotencyKey() }))
                await qc.invalidateQueries({ queryKey: ['rules'] })
                notifySuccess(`Rule “${r.name}” created`)
                void navigate({ to: '/rules/$ruleId', params: { ruleId: r.id } })
              }}
            />
          </Section>
        </Grid.Col>
        <Grid.Col span={{ base: 12, lg: 5 }}>
          <Section title="Preview" padded>
            <RulePreview definition={def} />
          </Section>
        </Grid.Col>
      </Grid>
    </>
  )
}
