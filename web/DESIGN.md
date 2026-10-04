# Waybill Guardian interface system

## 1. Visual theme and atmosphere

The interface is a logistics control tower for dispatch operators. It uses a light, dense
workspace with precise dividers, restrained elevation, and a single amber incident signal.
Green appears only after a write operation completes.

## 2. Color palette and roles

| Token | Value | Role |
|---|---|---|
| Canvas | `oklch(0.965 0.006 95)` | Page background |
| Surface | `oklch(0.992 0.003 95)` | Primary work surface |
| Ink | `oklch(0.215 0.018 255)` | Primary text |
| Muted ink | `oklch(0.49 0.018 255)` | Secondary text |
| Divider | `oklch(0.875 0.012 95)` | Structural separators |
| Signal | `oklch(0.69 0.145 67)` | Delay, anomaly, pending approval |
| Signal wash | `oklch(0.94 0.045 75)` | Pending background |
| Success | `oklch(0.55 0.11 155)` | Executed state |
| Danger | `oklch(0.56 0.16 28)` | Rejection and destructive action |

## 3. Typography rules

Use `"Avenir Next", "PingFang SC", "Noto Sans SC", sans-serif` for interface text and
`"SFMono-Regular", "JetBrains Mono", monospace` for identifiers and event metadata.
Headings use 20 to 28 px at weight 600. Body text uses 13 to 15 px with 1.7 line-height for
Chinese. Numeric values use tabular figures. Letter spacing remains zero.

## 4. Component styling

- Buttons use a 6 px radius, a 40 px minimum hit area, and `scale(0.96)` press feedback.
- Operational panels are flush sections separated by 1 px dividers.
- The approval object is the only elevated card because it is the human decision boundary.
- Inputs and range controls use the signal color for active state and a visible focus ring.
- Icon-only buttons use Lucide icons, tooltips, and accessible labels.

## 5. Layout principles

Use a 4 px base spacing scale. The CEO overview uses a compact header, a four-segment KPI
strip, a nationwide network map, a risk queue, and a read-only brief. The waybill workbench
uses one status strip and a two-column workspace. Its left column holds the map and timeline,
and its right column holds the decision boundary.

On the CEO overview, KPI segments form one flush band with dividers. They are not separate
cards. The risk queue stays beside the network map on desktop and moves below the map on
mobile. A hub or queue link opens `/waybills/:id`.

## 6. Depth and elevation

Canvas, surface, and raised approval layers differ by lightness. Only the approval card uses
`0 12px 32px oklch(0.22 0.02 255 / 0.10)`. No blur or glass effect is used.

## 7. Do and don't

- Do keep evidence and proposed effects visible without opening another surface.
- Do link the selected route point to its timestamp and speed.
- Do use amber only for an unresolved incident or approval.
- Do keep event sequence numbers aligned and readable.
- Don't place cards inside cards.
- Don't use a marketing hero or decorative illustration.
- Use the KPI band only on the CEO overview. Do not add a generic KPI tile grid to the
  waybill workbench.
- Don't animate layout properties.

## 8. Responsive behavior

At 1000 px the CEO map and risk queue become one column. At 720 px its KPI band becomes two
columns. At 960 px the waybill workspace becomes one column and approval moves above the map.
At 640 px the workbench header wraps, route metadata becomes a two-column grid, and playback
controls keep 40 px hit areas. Both pages must remain usable at 375 px and 320 px without
horizontal scrolling.

## 9. Agent prompt guide

- "Create a flush operations panel on `oklch(0.992 0.003 95)` with 1 px
  `oklch(0.875 0.012 95)` dividers, 8 px radius, and no shadow."
- "Create a pending approval card with a 6 px radius, `oklch(0.94 0.045 75)` header,
  13 px body text, and a single `oklch(0.69 0.145 67)` primary action."
- "Create a compact event row with a 48 px sequence column in monospace, 13 px event title,
  12 px metadata, and a 1 px divider."
