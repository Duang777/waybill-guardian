# Waybill Guardian interface system

## 1. Visual theme and atmosphere

The interface is a white industrial logistics table for dispatch operators. The nationwide
network is the first-viewport signal: a bright isometric 3D field with graphite labels,
desaturated teal infrastructure, and vermilion risk markers. Surrounding controls stay dense
and flush so the network remains the visual center. Green appears only after a write
operation completes.

BoardUI's public dashboard patterns informed the initial hierarchy study. The final
implementation uses original CSS Modules code, square industrial geometry, divider-led
structure, and no copied BoardUI source or components.

## 2. Color palette and roles

| Token | Value | Role |
|---|---|---|
| Canvas | `oklch(0.985 0.004 210)` | Page background |
| Surface | `oklch(1 0 0)` | Primary work surface |
| Ink | `oklch(0.215 0.018 255)` | Primary text |
| Muted ink | `oklch(0.49 0.018 255)` | Secondary text |
| Divider | `oklch(0.91 0.009 220)` | Structural separators |
| Network | `oklch(0.56 0.085 183)` | Hubs, normal routes, active controls |
| Network wash | `oklch(0.955 0.018 183)` | 3D field and selected network objects |
| Signal | `oklch(0.62 0.18 29)` | Delay, anomaly, pending approval |
| Signal wash | `oklch(0.96 0.035 29)` | Pending background |
| Success | `oklch(0.55 0.11 155)` | Executed state |
| Danger | `oklch(0.56 0.16 28)` | Rejection and destructive action |

## 3. Typography rules

Use `"Avenir Next", "PingFang SC", "Noto Sans SC", sans-serif` for interface text and
`"SFMono-Regular", "JetBrains Mono", monospace` for identifiers and event metadata.
Headings use 20 to 28 px at weight 600. Body text uses 13 to 15 px with 1.7 line-height for
Chinese. Numeric values use tabular figures. Letter spacing remains zero.

## 4. Component styling

- Buttons use a 4 px radius, a 40 px minimum hit area, and `scale(0.96)` press feedback.
- The 3D map and risk queue form one flush workspace separated by a 1 px divider.
- Queue views use plain text tabs with a 2 px active underline.
- Other operational sections remain flush and use 1 px dividers.
- The approval object is the only elevated card because it is the human decision boundary.
- Inputs and range controls use the signal color for active state and a visible focus ring.
- Icon-only buttons use Lucide icons, tooltips, and accessible labels.
- The local route fallback uses a pale coordinate field, a restrained route corridor, and a
  separate evidence rail. Evidence must never obscure the route.

## 5. Layout principles

Use a 4 px base spacing scale. The CEO overview uses a compact white header, a four-segment
KPI strip, a dominant nationwide 3D network, a narrow risk queue, and a read-only brief. The
waybill workbench uses one status strip and a two-column workspace. Its left column holds the
map and timeline, and its right column holds the decision boundary.

On the CEO overview, KPI segments form one flush band with dividers. They are not separate
cards. The network and queue share one flush work surface instead of nested cards. The
network must occupy at least two thirds of the desktop workspace
width and retain a stable 16:9-like field. The risk queue stays beside it on desktop and
moves below it on mobile. A risk hub or queue link opens `/waybills/:id`.

The risk queue exposes three views: all anomalies, unassigned anomalies, and active Agent
runs. Selection persists across views and the summary always reports both visible and selected
counts.

The 3D field uses an orthographic isometric camera, an abstract grid instead of a geographic
border, instanced hub columns, merged route geometry, and instanced moving shipment markers.
Do not use a dark skybox, bloom, glass panels, map tiles, or decorative gradients.

## 6. Depth and elevation

Canvas, flush work surface, and raised approval layers differ by lightness. Operational
surfaces use dividers without decorative shadows. The approval card uses
`0 12px 32px oklch(0.22 0.02 255 / 0.10)`. No blur or glass effect is used.

## 7. Do and don't

- Do keep evidence and proposed effects visible without opening another surface.
- Do link the selected route point to its timestamp and speed.
- Do use coral only for an unresolved incident or approval.
- Do keep event sequence numbers aligned and readable.
- Do keep the 3D network useful as a static frame under reduced motion.
- Do retain the SVG network as an automatic WebGL/error fallback.
- Do use segmented views when a dense operational list has stable, mutually exclusive modes.
- Don't place cards inside cards.
- Don't turn the KPI strip or intelligence brief into rounded dashboard cards.
- Don't use a marketing hero or decorative illustration.
- Don't use a dark header or orange/brown page palette on the overview.
- Use the KPI band only on the CEO overview. Do not add a generic KPI tile grid to the
  waybill workbench.
- Don't animate layout properties.

## 8. Responsive behavior

At 1000 px the CEO map and risk queue become one column. At 720 px its KPI band becomes two
columns. At 960 px the waybill workspace becomes one column and keeps route evidence before
the approval panel.
At 640 px the workbench header wraps, route metadata becomes a two-column grid, and playback
controls keep 40 px hit areas. Both pages must remain usable at 375 px and 320 px without
horizontal scrolling.

## 9. Agent prompt guide

- "Create a flush operations panel on `oklch(1 0 0)` with 1 px
  `oklch(0.91 0.009 220)` dividers, 4 px radius, and no shadow."
- "Create a pending approval card with a 4 px radius, `oklch(0.96 0.035 29)` header,
  13 px body text, and a single `oklch(0.62 0.18 29)` primary action."
- "Create a compact event row with a 48 px sequence column in monospace, 13 px event title,
  12 px metadata, and a 1 px divider."
- "Create three queue view tabs with transparent backgrounds, 40 px hit areas, 10 px labels,
  and a 2 px `oklch(0.215 0.018 255)` active underline."
