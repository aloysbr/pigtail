import type { Subject } from '@/api/types'
import { GROUP_MENU, GROUPINGS } from '@/domain/group'
import { canToggleStandby } from '@/domain/standby'
import type { Display } from '@/domain/status'
import { canLaunch } from '@/list/launch'
import { pickArea, pickGrouping } from '@/list/Menus'
import type { AreaItem } from '@/list/model'
import { commitChoice, readChoice, type Appearance, type PaletteName } from '@/theme/appearance'
import type { IconName } from '@/list/StatusIcon'
import { getUi, openSettings, toggleKeys, toggleSide } from '@/state/ui'
import { THEME_NAMES } from '@/ui/ThemeCard'

/** The subject actions a command may run (the shell's KeyActions fit). */
export interface SubjectActions {
  toggleStandby: (id: string) => void
  launch?: (id: string) => void
}

/** What a command may depend on: the shell's layout, and the selected subject with its display and actions, when the palette lists it. */
export interface PaletteCtx {
  docked: boolean
  wide: boolean
  phone: boolean
  /** The areas of the sidebar, in its order: « g puis n » and the « Aller à l'aire » commands. */
  areas: AreaItem[]
  selected?: Subject
  display?: Display
  actions?: SubjectActions
}

/** A palette command; the next lots (capture, launch) add theirs with one more registerCommand call. */
export interface CommandDef {
  id: string
  label: string | ((ctx: PaletteCtx) => string)
  /** A family of commands (navigation, actions…), kept for the lots that sort or split them. */
  group: string
  run: (ctx: PaletteCtx) => void
  when?: (ctx: PaletteCtx) => boolean
  /** The keys printed beside the label (« g 1 », « ⌘B »). */
  keys?: string
  /** A muted sub-text after the label (the scope of an area). */
  hint?: (ctx: PaletteCtx) => string | undefined
  /** Words the palette also matches (not shown): « thème » finds « Appearance: Dark ». */
  keywords?: string
  /** A short text after the label (« Actif »), drawn with a check in the ink colour, never a badge. */
  badge?: (ctx: PaletteCtx) => string | undefined
  /** The head icon of the row: a glyph of StatusIcon, or the swatch of a theme (drawn with that theme's own tokens). */
  icon?: CommandIcon
  /** The start of the label printed in bold (« Theme » in « Theme: Terra »). */
  lead?: string
}

export type CommandIcon = { glyph: IconName } | { theme: PaletteName }

/** A command as the palette lists it: its label resolved, its run bound to the context. */
export interface ListedCommand {
  id: string
  label: string
  group: string
  keys?: string
  hint?: string
  keywords?: string
  badge?: string
  icon?: CommandIcon
  lead?: string
  run: () => void
}

const registry: CommandDef[] = []

/** Adds a command at the end, or replaces in place the one with the same id. */
export function registerCommand(def: CommandDef) {
  const i = registry.findIndex((c) => c.id === def.id)
  if (i >= 0) registry[i] = def
  else registry.push(def)
}

/** Empties the registry (tests). */
export function clearCommands() {
  registry.length = 0
}

/** The commands available in a context, in registration order. */
export function commands(ctx: PaletteCtx): ListedCommand[] {
  return registry
    .filter((c) => !c.when || c.when(ctx))
    .map((c) => ({
      id: c.id,
      label: typeof c.label === 'function' ? c.label(ctx) : c.label,
      group: c.group,
      keys: c.keys,
      hint: c.hint?.(ctx),
      keywords: c.keywords,
      badge: c.badge?.(ctx),
      icon: c.icon,
      lead: c.lead,
      run: () => c.run(ctx),
    }))
}

/** The read-only web front's navigation commands (spec § Palette); safe to call more than once. */
export function registerBuiltins() {
  // nine slots, first: each lists itself only while the sidebar has an area at its index (D9)
  for (let i = 0; i < 9; i += 1) {
    registerCommand({
      id: `area:${i}`,
      label: (c) => `Aller à l'aire : ${c.areas[i]?.name ?? ''}`,
      hint: (c) => c.areas[i]?.scope,
      group: 'navigation',
      keys: `g puis ${i + 1}`,
      when: (c) => i < c.areas.length,
      run: (c) => {
        const a = c.areas[i]
        if (a) pickArea(a)
      },
    })
  }
  GROUPINGS.forEach((g, i) => {
    registerCommand({ id: `group:${g}`, label: `Grouper par : ${GROUP_MENU[g]}`, group: 'navigation', keys: `v puis ${i + 1}`, run: () => pickGrouping(g) })
  })
  registerCommand({
    id: 'sidebar',
    label: (c) => (c.docked || getUi().floatOpen ? 'Ranger la barre latérale' : 'Ouvrir la barre latérale'),
    group: 'navigation',
    keys: '⌘B',
    run: (c) => toggleSide(c.docked, c.wide),
  })
  registerCommand({
    id: 'keys',
    label: () => (getUi().kbHidden ? 'Afficher les raccourcis' : 'Masquer les raccourcis'),
    group: 'navigation',
    keys: ',',
    // the phone hides every key (t10 .mobile kbd): the footer has nothing to show there
    when: (c) => !c.phone,
    run: () => toggleKeys(),
  })
  registerThemeCommands()
  registerSubjectCommands()
}

/** Lancer and Mettre en standby / Reprendre for the selected subject (D15): listed only where the action is allowed, same labels as the buttons. */
export function registerSubjectCommands() {
  registerCommand({
    id: 'launch',
    label: 'Lancer',
    group: 'actions',
    keys: 'l',
    when: (c) => !!c.selected && !!c.display && !!c.actions?.launch && canLaunch(c.selected, c.display),
    run: (c) => {
      if (c.selected) c.actions?.launch?.(c.selected.id)
    },
  })
  registerCommand({
    id: 'standby',
    label: (c) => (c.selected?.status === 'parked' ? 'Reprendre' : 'Mettre en standby'),
    group: 'actions',
    keys: 'h',
    when: (c) => !!c.selected && !!c.actions && canToggleStandby(c.selected),
    run: (c) => {
      if (c.selected) c.actions?.toggleStandby(c.selected.id)
    },
  })
}

const THEME_KEYWORDS = 'thème apparence mode clair sombre système palette réglages'
const APPEARANCES: { value: Appearance; label: string; desc: string; icon: IconName }[] = [
  { value: 'light', label: 'Light', desc: 'Clair', icon: 'sun' },
  { value: 'dark', label: 'Dark', desc: 'Sombre', icon: 'moon' },
  { value: 'system', label: 'System', desc: 'Suit le Mac', icon: 'display' },
]

/** The theme switch of ⌘K (named in English, R16; descriptions in French): the same applyChoice / saveChoice path as the Réglages. */
export function registerThemeCommands() {
  const pick = (patch: Partial<{ palette: PaletteName; appearance: Appearance }>) => {
    commitChoice({ ...readChoice(), ...patch })
  }
  for (const id of ['terra', 'ocean'] as const) {
    registerCommand({
      id: `theme-${id}`,
      label: `Theme: ${THEME_NAMES[id].name}`,
      lead: 'Theme',
      icon: { theme: id },
      hint: () => THEME_NAMES[id].desc,
      keywords: THEME_KEYWORDS,
      badge: () => (readChoice().palette === id ? 'Actif' : undefined),
      group: 'theme',
      run: () => pick({ palette: id }),
    })
  }
  for (const a of APPEARANCES) {
    registerCommand({
      id: `appearance-${a.value}`,
      label: `Appearance: ${a.label}`,
      lead: 'Appearance',
      icon: { glyph: a.icon },
      hint: () => a.desc,
      keywords: THEME_KEYWORDS,
      badge: () => (readChoice().appearance === a.value ? 'Actif' : undefined),
      group: 'appearance',
      run: () => pick({ appearance: a.value }),
    })
  }
  registerCommand({
    id: 'open-settings',
    label: 'Open Settings',
    hint: () => 'thème et apparence',
    keywords: THEME_KEYWORDS,
    group: 'settings',
    keys: '⌘,',
    icon: { glyph: 'sliders' },
    run: () => openSettings(),
  })
}
