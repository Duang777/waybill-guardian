# Waybill Guardian interface system

## 1. Visual theme and atmosphere

Waybill Guardian is a dense logistics command center for dispatch operators and executives.
The default theme uses a near-black canvas, stepped graphite surfaces, and Transfar orange as
the only primary signal. Cool blue describes the transport network, green confirms completed
writes, and red marks failed or destructive states.

The interface stays operational rather than theatrical. It has no marketing hero, decorative
glow, glass cards, or nested card grids. The map and live operational data are the visual
anchors.

## 2. Color palette and roles

All source colors live in `src/styles/theme.css` as OKLCH custom properties. Component files
must consume semantic variables and must not contain raw color values.

| Token | Dark value | Role |
|---|---|---|
| `--background` | `oklch(0.135 0.009 250)` | Page canvas |
| `--surface-1` | `oklch(0.175 0.012 250)` | Primary workspace |
| `--surface-2` | `oklch(0.205 0.014 250)` | Raised operational panel |
| `--surface-3` | `oklch(0.245 0.016 250)` | Hover and selected surface |
| `--foreground` | `oklch(0.93 0.012 85)` | Primary text |
| `--muted-foreground` | `oklch(0.69 0.014 250)` | Secondary text |
| `--border` | `oklch(0.34 0.016 250)` | Structural divider |
| `--primary` | `oklch(0.72 0.18 50)` | Pending approval and main action |
| `--success` | `oklch(0.72 0.14 155)` | Completed write |
| `--danger` | `oklch(0.57 0.19 28)` | Failure and destructive action |
| `--info` | `oklch(0.70 0.10 230)` | Normal route and network context |

The `.light` class provides the print and bright-projector theme with the same semantic token
names. Dark mode remains the default so local demos require no preference bootstrap.

## 3. Typography rules

Use the self-hosted `"WG Sans SC"` subset of Noto Sans SC for interface text and
`"JetBrains Mono Variable"` for identifiers, event sequence numbers, timestamps, and numeric
metrics. The committed 400, 500, and 700 weight subsets cover runtime UI and fixture text,
use `font-display: swap`, and can be regenerated with `npm run font:subset`.

| Role | Size | Weight | Line height |
|---|---:|---:|---:|
| Page heading | 20 to 28 px | 650 | 1.2 |
| Panel heading | 16 to 18 px | 650 | 1.35 |
| Body | 13 to 15 px | 400 to 500 | 1.7 |
| Metadata | 10 to 12 px | 500 to 650 | 1.4 |
| KPI | 28 to 40 px | 650 | 1.05 |

Chinese text keeps zero letter spacing. Numeric values use tabular figures. Compact labels may
use uppercase English only when they also have a Chinese heading beside them.

## 4. Component styling

- Buttons use `--radius-md`, a 40 px minimum hit area, a visible focus ring, and a 120 ms
  `scale(0.96)` press response. Hover styles run only on hover-capable devices.
- Operational panels are flush sections separated by 1 px dividers. A panel uses a raised
  surface only when it needs to separate an action boundary from surrounding evidence.
- The approval object is the only elevated card because it is the human decision boundary.
- Inputs use `--surface-1`, `--border`, and the orange focus ring. Disabled controls reduce
  opacity but retain readable text.
- Icon-only buttons use Lucide icons, tooltips, and accessible labels.
- Pending, success, danger, and information states use their semantic wash and text tokens.
  Orange never denotes a completed operation.

## 5. Layout principles

The spacing scale is based on 4 px and defined in `theme.css`. Optical half and quarter steps
exist for dense controls, but components must reference a spacing token rather than a raw
length. The radius scale is `3 / 4 / 6 / 8 / full`.

The CEO overview uses a compact header, one flush four-segment KPI band, a nationwide network
map, a risk queue, and a read-only brief. The waybill workbench uses one status strip and a
two-column workspace. Its left column contains the map and audit timeline, while its right
column contains the approval boundary.

Tailwind CSS v4 owns global tokens, reset styles, and new utility styling. Existing CSS
Modules keep component layout during migration. Do not apply a CSS Module class and Tailwind
utility classes to the same element.

## 6. Depth and elevation

Dark-mode depth comes from surface lightness steps, not dark drop shadows. Use two elevation
levels only:

1. `--shadow-panel` outlines a raised operational panel.
2. `--shadow-elevated` separates the approval boundary and map inspector.

Orange and green glows are reserved for unresolved approval and completed connection status.
Do not use blur, backdrop filters, or decorative shadows.

## 7. Do and don't

- Keep evidence and proposed effects visible without opening another surface.
- Link the selected route point to its timestamp and speed.
- Use orange only for an unresolved incident, an approval, or the primary action.
- Keep event sequence numbers and metric digits aligned.
- Keep the real map, network, or operational data as each view's visual anchor.
- Do not place cards inside cards.
- Do not use a marketing hero, decorative illustration, gradient text, or glow decoration.
- Use the KPI band only on the CEO overview.
- Do not animate layout properties or use `transition: all`.
- Do not add raw colors, spacing values, radii, font stacks, or shadows outside `theme.css`.

## 8. Responsive behavior

At 1000 px the CEO map and risk queue become one column. At 720 px its KPI band becomes two
columns. At 960 px the waybill workspace becomes one column and approval moves above the map.
At 640 px the workbench header wraps, route metadata becomes a two-column grid, and playback
controls retain 40 px hit areas. Both pages must remain usable at 375 px and 320 px without
horizontal scrolling.

Desktop verification uses 1280 x 900 and 1600 x 900. Mobile verification uses 375 x 812 and
320 x 812. Later large-screen work may add 1920 x 1080 without changing these baselines.

## 9. Agent prompt guide

- "Create a flush command-center panel on `--surface-1`, separated by `--border-subtle`,
  using `--radius-lg`, no shadow, and spacing tokens from `theme.css`."
- "Create a pending approval boundary on `--surface-2` with `--primary-border`, a
  `--primary-wash` header, `--shadow-elevated`, 13 px body text, and one `--primary` action."
- "Create a compact audit row with a 48 px mono sequence column, 13 px event title, 12 px
  metadata, and a `--border-subtle` divider."
- "Create a network metric using `--info` for normal transport state, `--primary` for delay,
  `--danger` for failure, tabular numbers, and no decorative gradient."
