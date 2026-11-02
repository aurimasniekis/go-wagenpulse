import { Button, Group, Modal, Stack, Text, TextInput } from '@mantine/core'
import { useState, type ReactNode } from 'react'

/**
 * Confirmation dialog for consequential actions. When `typeToConfirm` is set the user must type it (destructive,
 * irreversible actions such as purges and token revocation).
 */
export function ConfirmDialog({
  opened,
  onClose,
  onConfirm,
  title,
  children,
  confirmLabel,
  danger = false,
  typeToConfirm,
  loading = false,
}: {
  opened: boolean
  onClose: () => void
  onConfirm: () => void
  title: string
  children: ReactNode
  confirmLabel: string
  danger?: boolean
  typeToConfirm?: string
  loading?: boolean
}) {
  const [typed, setTyped] = useState('')
  const ok = !typeToConfirm || typed.trim() === typeToConfirm
  return (
    <Modal
      opened={opened}
      onClose={() => {
        setTyped('')
        onClose()
      }}
      title={<Text fw={650}>{title}</Text>}
      size="md"
    >
      <Stack gap="md">
        <div style={{ fontSize: 'var(--mantine-font-size-sm)' }}>{children}</div>
        {typeToConfirm ? (
          <TextInput
            label={
              <span>
                Type <code>{typeToConfirm}</code> to confirm
              </span>
            }
            value={typed}
            onChange={(e) => {
              setTyped(e.currentTarget.value)
            }}
            autoComplete="off"
            data-autofocus
          />
        ) : null}
        <Group justify="flex-end" gap="sm">
          <Button
            variant="default"
            onClick={() => {
              setTyped('')
              onClose()
            }}
          >
            Cancel
          </Button>
          <Button
            color={danger ? 'red' : undefined}
            disabled={!ok}
            loading={loading}
            onClick={() => {
              onConfirm()
              setTyped('')
            }}
          >
            {confirmLabel}
          </Button>
        </Group>
      </Stack>
    </Modal>
  )
}
