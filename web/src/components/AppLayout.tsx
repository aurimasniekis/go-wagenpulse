import {
  ActionIcon,
  AppShell,
  Avatar,
  Burger,
  Divider,
  Group,
  Menu,
  NavLink,
  ScrollArea,
  Text,
  Tooltip,
  UnstyledButton,
  useMantineColorScheme,
  VisuallyHidden,
} from '@mantine/core'
import { useDisclosure } from '@mantine/hooks'
import {
  IconAdjustmentsHorizontal,
  IconBolt,
  IconBook2,
  IconCar,
  IconChartLine,
  IconClipboardList,
  IconClockHour4,
  IconDeviceDesktop,
  IconFileImport,
  IconGauge,
  IconKey,
  IconLogout,
  IconMoon,
  IconPlugConnected,
  IconRoute,
  IconServer2,
  IconShieldLock,
  IconSun,
  IconUser,
  IconUsers,
  IconWorld,
  IconDatabaseImport,
} from '@tabler/icons-react'
import { useSelector } from '@tanstack/react-store'
import { Link, useNavigate, useRouterState } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import { api, check, setCsrfToken, type Me } from '@/api/client'
import { notifyError } from '@/lib/notify'
import { prefs, setPref } from '@/lib/prefs'
import { MenuItemLink } from './links'

interface NavItem {
  to: string
  label: string
  icon: ReactNode
  perm: string
}

const sections: { title: string; items: NavItem[] }[] = [
  {
    title: 'Monitor',
    items: [
      { to: '/', label: 'Fleet', icon: <IconGauge size={18} />, perm: 'vehicles.read' },
      { to: '/vehicles', label: 'Vehicles', icon: <IconCar size={18} />, perm: 'vehicles.read' },
      { to: '/explorer', label: 'Data explorer', icon: <IconChartLine size={18} />, perm: 'telemetry.read' },
      { to: '/signals', label: 'Signal dictionary', icon: <IconBook2 size={18} />, perm: 'signals.read' },
    ],
  },
  {
    title: 'Ingest',
    items: [
      { to: '/imports', label: 'Imports', icon: <IconFileImport size={18} />, perm: 'imports.read' },
      { to: '/sources', label: 'Sources', icon: <IconDatabaseImport size={18} />, perm: 'sources.read' },
    ],
  },
  {
    title: 'Events',
    items: [
      { to: '/rules', label: 'Rules', icon: <IconBolt size={18} />, perm: 'rules.read' },
      { to: '/events', label: 'Events & deliveries', icon: <IconClipboardList size={18} />, perm: 'events.read' },
      { to: '/adapters', label: 'Adapters', icon: <IconPlugConnected size={18} />, perm: 'adapters.read' },
      { to: '/routing', label: 'Routing', icon: <IconRoute size={18} />, perm: 'routes.read' },
    ],
  },
  {
    title: 'Administration',
    items: [
      { to: '/admin/users', label: 'Users', icon: <IconUsers size={18} />, perm: 'users.read' },
      { to: '/admin/tokens', label: 'API tokens', icon: <IconKey size={18} />, perm: 'tokens.own' },
      { to: '/admin/sso', label: 'Single sign-on', icon: <IconShieldLock size={18} />, perm: 'oidc.read' },
      {
        to: '/admin/settings',
        label: 'Settings',
        icon: <IconAdjustmentsHorizontal size={18} />,
        perm: 'settings.read',
      },
      { to: '/admin/audit', label: 'Audit log', icon: <IconClockHour4 size={18} />, perm: 'audit.read' },
      { to: '/admin/system', label: 'System', icon: <IconServer2 size={18} />, perm: 'system.read' },
    ],
  },
]

function Brand() {
  return (
    <Group gap={10} wrap="nowrap">
      <svg width="26" height="26" viewBox="0 0 32 32" aria-hidden="true">
        <rect width="32" height="32" rx="6" fill="#193549" />
        <path
          d="M5 18h5l3-8 5 14 3-9 2 3h4"
          fill="none"
          stroke="#ffc600"
          strokeWidth="2.6"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
      <Text fw={700} size="lg" lts="-0.01em">
        Wagen<span style={{ color: 'var(--wp-accent-text)' }}>Pulse</span>
      </Text>
    </Group>
  )
}

function ThemeToggle() {
  const { colorScheme, setColorScheme } = useMantineColorScheme()
  const next = colorScheme === 'light' ? 'dark' : colorScheme === 'dark' ? 'auto' : 'light'
  const icon =
    colorScheme === 'light' ? (
      <IconSun size={18} />
    ) : colorScheme === 'dark' ? (
      <IconMoon size={18} />
    ) : (
      <IconDeviceDesktop size={18} />
    )
  return (
    <Tooltip label={`Theme: ${colorScheme} (switch to ${next})`}>
      <ActionIcon
        variant="default"
        size="lg"
        aria-label={`Theme: ${colorScheme}. Switch to ${next}`}
        onClick={() => {
          setColorScheme(next)
        }}
      >
        {icon}
      </ActionIcon>
    </Tooltip>
  )
}

function ZoneToggle() {
  const zone = useSelector(prefs, (s) => s.timeZone)
  const label = zone === 'UTC' ? 'UTC' : Intl.DateTimeFormat().resolvedOptions().timeZone
  return (
    <Tooltip label={`Times shown in ${label}. Click to switch.`}>
      <UnstyledButton
        onClick={() => {
          setPref('timeZone', zone === 'UTC' ? 'local' : 'UTC')
        }}
        aria-label={`${zone === 'UTC' ? 'UTC' : 'Local'} time (${label}): switch time zone`}
        px={8}
        py={4}
        style={{ border: '1px solid var(--wp-border)', borderRadius: 4 }}
      >
        <Group gap={6} wrap="nowrap">
          <IconWorld size={16} />
          <Text size="xs" fw={600}>
            {zone === 'UTC' ? 'UTC' : 'Local'}
          </Text>
        </Group>
      </UnstyledButton>
    </Tooltip>
  )
}

function UserMenu({ me }: { me: Me }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const logout = async () => {
    try {
      check(await api.POST('/auth/logout'))
    } catch (e) {
      notifyError(e)
    }
    setCsrfToken('')
    qc.clear()
    void navigate({ to: '/login' })
  }
  return (
    <Menu position="bottom-end" width={220}>
      <Menu.Target>
        <UnstyledButton>
          <Group gap={8} wrap="nowrap">
            <Avatar size={30} radius="sm" color="cobalt" aria-hidden="true">
              {me.user.display_name.slice(0, 2).toUpperCase()}
            </Avatar>
            <div style={{ lineHeight: 1.1 }}>
              <Text size="sm" fw={600} visibleFrom="sm">
                {me.user.display_name}
              </Text>
              <Text size="xs" c="dimmed" visibleFrom="sm">
                {me.user.role}
              </Text>
            </div>
            <VisuallyHidden>Account menu</VisuallyHidden>
          </Group>
        </UnstyledButton>
      </Menu.Target>
      <Menu.Dropdown>
        <Menu.Label>{me.user.username}</Menu.Label>
        <MenuItemLink to="/account" leftSection={<IconUser size={16} />}>
          Account & security
        </MenuItemLink>
        <Menu.Divider />
        <Menu.Item color="red" leftSection={<IconLogout size={16} />} onClick={() => void logout()}>
          Sign out
        </Menu.Item>
      </Menu.Dropdown>
    </Menu>
  )
}

export function AppLayout({ me, children }: { me: Me; children: ReactNode }) {
  const [opened, { toggle, close }] = useDisclosure()
  const path = useRouterState({ select: (s) => s.location.pathname })
  const perms = new Set(me.permissions)
  const isActive = (to: string) => (to === '/' ? path === '/' : path === to || path.startsWith(`${to}/`))
  return (
    <AppShell
      header={{ height: 56 }}
      navbar={{ width: 248, breakpoint: 'md', collapsed: { mobile: !opened } }}
      padding="lg"
    >
      <a href="#main" className="wp-skip-link">
        Skip to content
      </a>
      <AppShell.Header style={{ background: 'var(--wp-surface)', borderColor: 'var(--wp-border)' }}>
        <Group h="100%" px="md" justify="space-between" wrap="nowrap">
          <Group gap="sm" wrap="nowrap">
            <Burger opened={opened} onClick={toggle} hiddenFrom="md" size="sm" aria-label="Toggle navigation" />
            <Link to="/" style={{ textDecoration: 'none', color: 'inherit' }} aria-label="WagenPulse home">
              <Brand />
            </Link>
          </Group>
          <Group gap="sm" wrap="nowrap">
            <ZoneToggle />
            <ThemeToggle />
            <UserMenu me={me} />
          </Group>
        </Group>
      </AppShell.Header>
      <AppShell.Navbar
        p="sm"
        style={{ background: 'var(--wp-surface2)', borderColor: 'var(--wp-border)' }}
        aria-label="Main navigation"
      >
        <AppShell.Section grow component={ScrollArea}>
          {sections.map((sec, i) => {
            const items = sec.items.filter((it) => perms.has(it.perm))
            if (items.length === 0) return null
            return (
              <div key={sec.title}>
                {i > 0 ? <Divider my="xs" color="var(--wp-border)" /> : null}
                <Text size="xs" fw={700} c="dimmed" tt="uppercase" lts="0.06em" px="sm" py={6}>
                  {sec.title}
                </Text>
                {items.map((it) => (
                  <NavLink
                    key={it.to}
                    component={Link}
                    to={it.to}
                    label={it.label}
                    leftSection={it.icon}
                    active={isActive(it.to)}
                    onClick={close}
                    style={{ borderRadius: 4 }}
                    variant="light"
                  />
                ))}
              </div>
            )
          })}
        </AppShell.Section>
        <AppShell.Section>
          <Text size="xs" c="dimmed" px="sm" pt="xs">
            Data via the EU Data Act portal (unofficial interface for collection)
          </Text>
        </AppShell.Section>
      </AppShell.Navbar>
      <AppShell.Main id="main" tabIndex={-1} style={{ outline: 'none' }}>
        {children}
      </AppShell.Main>
    </AppShell>
  )
}
