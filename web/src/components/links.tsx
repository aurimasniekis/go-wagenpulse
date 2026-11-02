import {
  Anchor,
  Button,
  Card,
  Menu,
  type AnchorProps,
  type ButtonProps,
  type CardProps,
  type MenuItemProps,
} from '@mantine/core'
import { createLink, type LinkComponent } from '@tanstack/react-router'
import type { AnchorHTMLAttributes, Ref } from 'react'

// Mantine components rendered as type-safe TanStack Router links (route paths, params and search are checked).

type A = AnchorHTMLAttributes<HTMLAnchorElement> & { ref?: Ref<HTMLAnchorElement> }

const AnchorBase = (props: AnchorProps & A) => <Anchor component="a" {...props} />
const CreatedAnchor = createLink(AnchorBase)
export const AnchorLink: LinkComponent<typeof AnchorBase> = (props) => <CreatedAnchor preload="intent" {...props} />

const ButtonBase = (props: ButtonProps & A) => <Button component="a" {...props} />
const CreatedButton = createLink(ButtonBase)
export const ButtonLink: LinkComponent<typeof ButtonBase> = (props) => <CreatedButton preload="intent" {...props} />

const CardBase = (props: CardProps & A) => <Card component="a" {...props} />
const CreatedCard = createLink(CardBase)
export const CardLink: LinkComponent<typeof CardBase> = (props) => <CreatedCard preload="intent" {...props} />

const MenuItemBase = (props: MenuItemProps & A) => <Menu.Item component="a" {...props} />
const CreatedMenuItem = createLink(MenuItemBase)
export const MenuItemLink: LinkComponent<typeof MenuItemBase> = (props) => <CreatedMenuItem {...props} />
