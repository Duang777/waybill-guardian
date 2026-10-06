# Waybill Guardian interface system

## 1. Visual theme and atmosphere

The interface is a paper-white editorial operations table for dispatch operators. On the
overview, a large serif situation title and four divided numeric indexes establish the
reading order before the nationwide network. The bright isometric 3D field remains the
first interactive signal, with graphite labels, desaturated teal infrastructure, and
vermilion risk markers. Surrounding controls stay dense and flush. Green appears only after
a write operation completes.

The waybill workbench continues the same editorial treatment with black rules, a large serif
route title, and a full-width geographic evidence stage. Its reading order is route status,
map evidence, audit history, then the human decision boundary. Vermilion marks only the
unresolved incident. Both the route point inspector and the selected facility summary use a
single dark caption rail so the selected evidence remains visually anchored to its map.

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
The overview situation title, section headings, route title, and approval title use
`"Songti SC", "STSong", "Noto Serif CJK SC", serif`. The overview title is 44 px on desktop
and 32 px on mobile. The route title is 64 px on desktop. Operational section headings are
25 to 32 px at weight 600. Body text uses 13 to 15 px with 1.7 line-height for Chinese.
Numeric values use tabular figures. Letter spacing remains zero. Font sizes are fixed per
breakpoint and never scale with viewport width.

## 4. Component styling

- Buttons use square geometry, a 40 px minimum hit area, and `scale(0.96)` press feedback.
- The overview masthead stays paper-white. It never becomes a dark dashboard banner.
- KPI indexes use one divided band with bottom-aligned labels and 32 px numeric values.
- The 3D map and risk queue form one flush workspace separated by a 1 px divider.
- Queue views use plain text tabs with a 2 px active underline.
- Facility selection uses a square charcoal caption rail instead of a floating white card.
- Other operational sections remain flush and use 1 px dividers.
- The waybill approval boundary is a rule-separated column, not an elevated card.
- Inputs and range controls use the signal color for active state and a visible focus ring.
- Icon-only buttons use Lucide icons, tooltips, and accessible labels.
- The waybill route uses MapLibre with an OpenFreeMap light vector style when no AMap key is
  configured. The local SVG fallback uses a pale coordinate field and the same separate
  evidence rail. Evidence must never obscure the route.

## 5. Layout principles

Use a 4 px base spacing scale. The CEO overview starts with an editorial masthead beside a
four-segment KPI index, followed by a dominant nationwide 3D network, a narrow risk queue,
and a read-only brief. The waybill workbench starts with a larger route masthead. The route
occupies the dominant left field, while shipment facts and risk bars form a narrow right
index. Below it, a two-column workspace keeps the geographic map and audit timeline on the
left and the decision boundary on the right. The map and its dark point-inspector rail are
one 500 px evidence stage.

On the CEO overview, KPI segments form one flush band with dividers. They are not separate
cards. The network and queue share one flush work surface instead of nested cards. The
network must occupy at least two thirds of the desktop workspace
width and retain a stable 16:9-like field. The risk queue stays beside it on desktop and
moves below it on mobile. A risk hub or queue link opens `/waybills/:id`.

The risk queue exposes three views: all anomalies, unassigned anomalies, and active Agent
runs. Selection persists across views and the summary always reports both visible and selected
counts.

The 3D field uses an orthographic isometric camera, an abstract grid instead of a geographic
border, instanced facility parts, merged route geometry, and instanced moving shipment
markers. Four facility archetypes reuse warehouse, dock, yard, tower, and signal parts.
Daily capacity selects the archetype and footprint, in-flight volume fills cargo bays,
anomalies raise a red signal mast, and the primary route sets the facility orientation.
Selecting a hub replaces the nationwide layers with one enlarged facility inspection view.
That view uses five road and warehouse topologies: long-haul linear, cross-dock, courtyard,
split-yard, and gateway. Capacity, route distance, location, throughput, in-flight volume, and
risk determine the campus proportions, warehouse arrangement, docks, storage slots, entrance,
and alert tower. The same hub must keep a stable geometry signature, while different hubs must
not share one.
Do not use a dark skybox, bloom, glass panels, or decorative gradients. Geographic map tiles
are reserved for the waybill evidence view; the CEO network remains an abstract 3D field.

## 6. Depth and elevation

Canvas and flush work surfaces differ by lightness. Operational surfaces and the approval
boundary use dividers without decorative shadows. The dark point inspector uses contrast
instead of elevation. No blur or glass effect is used.

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
- Don't reduce the waybill route masthead to a generic dashboard status card.
- Use the KPI band only on the CEO overview. Do not add a generic KPI tile grid to the
  waybill workbench.
- Don't animate layout properties.

## 8. Responsive behavior

At 1000 px the CEO map and risk queue become one column. At 720 px its KPI band becomes two
columns and its situation title moves above the indexes. At 420 px the situation title is
32 px and the 3D stage remains at least 360 px tall. At 960 px the waybill workspace becomes
one column and keeps route evidence before the approval panel. Between 640 px and 960 px, the
approval heading becomes a dark side rail. At 640 px the approval returns to one column, the
route title becomes 36 px, and the map keeps a stable 300 px viewport above its inspector.
The facility inspection view keeps the selected campus centered and places its detail summary
above the bottom legend so neither layer obscures the model.
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
