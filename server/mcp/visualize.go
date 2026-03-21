package mcp

import (
	"context"
	"log"
	"strings"

	gomcp "github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// visualize_read_me handler
// ---------------------------------------------------------------------------

func (s *Server) handleVisualizeReadMe(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()

	// Parse modules array
	var modules []string
	if raw, ok := args["modules"]; ok {
		if arr, ok := raw.([]interface{}); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					modules = append(modules, s)
				}
			}
		}
	}

	// Build response: always include core, then module-specific sections
	var sb strings.Builder
	sb.WriteString(visualizeCoreDoc)

	moduleSet := make(map[string]bool)
	for _, m := range modules {
		moduleSet[m] = true
	}

	if moduleSet["diagram"] {
		sb.WriteString(visualizeDiagramDoc)
	}
	if moduleSet["mockup"] || moduleSet["interactive"] {
		sb.WriteString(visualizeMockupDoc)
	}
	if moduleSet["chart"] || moduleSet["data_viz"] {
		sb.WriteString(visualizeMockupDoc)
		sb.WriteString(visualizeChartDoc)
	}
	if moduleSet["art"] {
		sb.WriteString(visualizeArtDoc)
	}

	log.Printf("[mcp] visualize_read_me: modules=%v", modules)
	return textResult(sb.String()), nil
}

// ---------------------------------------------------------------------------
// visualize_show_widget handler
// ---------------------------------------------------------------------------

func (s *Server) handleVisualizeShowWidget(ctx context.Context, request gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := request.GetArguments()

	title, _ := args["title"].(string)
	widgetCode, _ := args["widget_code"].(string)

	if widgetCode == "" {
		return errorResult("widget_code is required"), nil
	}

	log.Printf("[mcp] visualize_show_widget: title=%s code_len=%d", title, len(widgetCode))

	// The actual rendering is done by the frontend.
	// The backend eventHandler intercepts this tool call and emits a widget event.
	return textResult(`{"ok":true}`), nil
}

// ---------------------------------------------------------------------------
// Design system documentation constants
// ---------------------------------------------------------------------------

const visualizeCoreDoc = `# BeeHub Visual Creation Suite

## Modules
Call visualize_read_me again with the modules parameter to load detailed guidance:
- diagram — SVG flowcharts, structural diagrams, illustrative diagrams
- mockup — UI mockups, forms, cards, dashboards
- interactive — interactive explainers with controls
- chart — charts, data analysis, geographic maps (Chart.js, D3 choropleth)
- art — illustration and generative art

## Core Design System

### Philosophy
- **Seamless**: The widget should feel like a natural extension of the chat.
- **Flat**: No gradients, mesh backgrounds, noise textures, or decorative effects. Clean flat surfaces.
- **Compact**: Show the essential inline. Explain the rest in text.
- **Text goes in your response, visuals go in the tool** — All explanatory text must be written as normal response text OUTSIDE the tool call. The tool output should contain ONLY the visual element.

### Streaming
Output streams token-by-token. Structure code so useful content appears early.
- **HTML**: <style> (short) → content HTML → <script> last.
- **SVG**: <defs> (markers) → visual elements immediately.
- Prefer inline style="..." over <style> blocks.
- Keep <style> under ~15 lines.
- No gradients, shadows, or blur (flash during streaming).

### Rules
- No <!-- comments --> or /* comments */ (waste tokens, break streaming)
- No font-size below 11px
- No emoji — use CSS shapes or SVG paths
- No gradients, drop shadows, blur, glow, or neon effects
- No dark/colored backgrounds on outer containers (transparent only — host provides the bg)
- **Typography**: Use system sans-serif font. For serif, use font-family: Georgia, serif.
- **Headings**: h1 = 22px, h2 = 18px, h3 = 16px — all font-weight: 500. Body text = 16px, weight 400, line-height: 1.7. Two weights only: 400 regular, 500 bold.
- **Sentence case** always. Never Title Case, never ALL CAPS.
- No mid-sentence bolding. Entity names, class names, function names go in code style not bold.
- The widget container is display: block; width: 100%. Your HTML fills it naturally.
- Never use position: fixed — the iframe viewport sizes itself to in-flow content height.
- No DOCTYPE, <html>, <head>, or <body> — just content fragments.
- **Corners**: use border-radius: 8px for elements, 12px for cards. In SVG, rx="4" default.
- No rounded corners on single-sided borders.
- No titles or prose inside the tool output.
- **Icon sizing**: 16px for inline icons, 24px max for decorative icons.
- No tabs, carousels, or display: none sections during streaming.
- No nested scrolling — auto-fit height.
- Scripts execute after streaming — load libraries from CDN.
- **Available libraries** (load via CDN):
  - Chart.js: https://cdn.jsdelivr.net/npm/chart.js@4/dist/chart.umd.min.js
  - D3.js: https://cdn.jsdelivr.net/npm/d3@7/dist/d3.min.js
  - TopoJSON: https://cdn.jsdelivr.net/npm/topojson-client@3/dist/topojson-client.min.js

### CSS Variables
**Backgrounds**: --color-bg-primary (white), --color-bg-secondary (surfaces), --color-bg-tertiary (page bg)
**Text**: --color-text-primary (black), --color-text-secondary (muted), --color-text-tertiary (hints)
**Borders**: --color-border-primary, --color-border-secondary, --color-border-tertiary
**Semantic**: --color-info, --color-success, --color-warning, --color-danger
**Layout**: --border-radius-md (8px), --border-radius-lg (12px)

All auto-adapt to light/dark mode. For custom colors in HTML, use CSS variables.

**Dark mode is mandatory** — every color must work in both modes:
- In HTML: always use CSS variables for text. Never hardcode colors like color: #333.
- Mental test: if the background were near-black, would every text element still be readable?

### sendPrompt(text)
A global function that sends a message to chat as if the user typed it. Use it when the user's next step benefits from AI thinking.

## Color Palette

9 color ramps, each with 7 stops from lightest to darkest (50 = lightest, 900 = darkest):

| Name | 50 | 100 | 200 | 400 | 600 | 800 | 900 |
|------|------|------|------|------|------|------|------|
| purple | #EEEDFE | #CECBF6 | #AFA9EC | #7F77DD | #534AB7 | #3C3489 | #26215C |
| teal | #E1F5EE | #9FE1CB | #5DCAA5 | #1D9E75 | #0F6E56 | #085041 | #04342C |
| coral | #FAECE7 | #F5C4B3 | #F0997B | #D85A30 | #993C1D | #712B13 | #4A1B0C |
| pink | #FBEAF0 | #F4C0D1 | #ED93B1 | #D4537E | #993556 | #72243E | #4B1528 |
| gray | #F1EFE8 | #D3D1C7 | #B4B2A9 | #888780 | #5F5E5A | #444441 | #2C2C2A |
| blue | #E6F1FB | #B5D4F4 | #85B7EB | #378ADD | #185FA5 | #0C447C | #042C53 |
| green | #EAF3DE | #C0DD97 | #97C459 | #639922 | #3B6D11 | #27500A | #173404 |
| amber | #FAEEDA | #FAC775 | #EF9F27 | #BA7517 | #854F0B | #633806 | #412402 |
| red | #FCEBEB | #F7C1C1 | #F09595 | #E24B4A | #A32D2D | #791F1F | #501313 |

**Color assignment**: Color encodes meaning, not sequence. Group nodes by category. Use 2-3 colors per diagram. Gray for neutral/structural nodes.

**Text on colored backgrounds**: Use the 800 or 900 stop from the same ramp. Never use black or gray on colored fills.
`

const visualizeDiagramDoc = `
## SVG Diagrams

**SVG setup**: <svg width="100%" viewBox="0 0 680 H"> — 680px wide, flexible height. Set H to fit content tightly. Safe area: x=40 to x=640, y=40 to y=(H-40). Background transparent. Output the raw <svg> element directly.

**Do not change viewBox width from 680** — it matches the widget container for 1:1 rendering.

**Pre-built classes** (injected into SVG widget):
- class="t" = sans 14px primary, class="ts" = sans 12px secondary, class="th" = sans 14px medium (500)
- class="box" = neutral rect (bg-secondary fill, border stroke)
- class="arr" = arrow line (1.5px, open chevron head)
- Colors: c-purple, c-teal, c-coral, c-pink, c-gray, c-blue, c-green, c-amber, c-red

**Arrow marker**: always include at the start of every SVG:
<defs><marker id="arrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="6" markerHeight="6" orient="auto-start-reverse"><path d="M2 1L8 5L2 9" fill="none" stroke="context-stroke" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></marker></defs>

**Key rules**:
- Every <text> needs a class (t, ts, or th)
- Two font sizes only: 14px (labels) and 12px (subtitles, descriptions)
- Sentence case on all labels
- Box subtitles: ≤5 words
- Colors: ≤2 ramps per diagram
- Stroke width: 0.5px for borders and edges
- Connector paths need fill="none"
- Rect rounding: rx="4" for subtle, rx="8" max
- No rotated text
- Check arrow paths don't cross through unrelated boxes
- Compute box width from text: rect_width = max(title_chars × 8, subtitle_chars × 7) + 24
`

const visualizeMockupDoc = `
## UI Components

### Aesthetic
Flat, clean, white surfaces. Minimal 0.5px borders. Generous whitespace. No gradients, no shadows.

### Tokens
- Borders: always 0.5px solid var(--color-border-tertiary) (or secondary for emphasis)
- Corner radius: 8px for most elements, 12px for cards
- Cards: white bg, 0.5px border, 12px radius, padding 1rem 1.25rem
- Form elements (input, select, textarea, button, range slider) — write bare tags, style inline as needed
- Buttons: transparent bg, 0.5px border, hover effect. If it triggers sendPrompt, append a ↗ arrow.
- Round every displayed number (Math.round, toFixed, etc.)
- Spacing: rem for vertical rhythm, px for component-internal gaps

### Metric Cards
For summary numbers: surface card with muted 13px label, 24px/500 number. background: var(--color-bg-secondary), no border, 8px radius, padding 1rem.

### Layout
- Editorial (explanatory): no card wrapper, prose flows naturally
- Card (bounded objects): single raised card wraps the whole thing
- Don't put tables inside — output them as markdown in response text

### Interactive Explainer
Use sliders, buttons, live state displays, charts. Keep prose in your response text. Use sendPrompt() for follow-ups.

### Compare Options
Side-by-side card grid. Use repeat(auto-fit, minmax(160px, 1fr)). Add sendPrompt() buttons.
`

const visualizeChartDoc = `
## Charts (Chart.js)

<div style="position: relative; width: 100%; height: 300px;">
  <canvas id="myChart"></canvas>
</div>
<script src="https://cdn.jsdelivr.net/npm/chart.js@4/dist/chart.umd.min.js"></script>
<script>
  new Chart(document.getElementById('myChart'), {
    type: 'bar',
    data: { labels: ['Q1','Q2','Q3','Q4'], datasets: [{ label: 'Revenue', data: [12,19,8,15] }] },
    options: { responsive: true, maintainAspectRatio: false }
  });
</script>

**Chart.js rules**:
- Canvas cannot resolve CSS variables. Use hardcoded hex.
- Wrap <canvas> in <div> with explicit height and position: relative.
- Set height ONLY on the wrapper div, never on the canvas.
- Load via <script src="https://cdn.jsdelivr.net/npm/chart.js@4/dist/chart.umd.min.js"></script>.
- Multiple charts: use unique IDs.
- Always disable default legend and build custom HTML legend.

## Geographic Maps (D3 Choropleth)

Load D3 and TopoJSON from CDN:
<script src="https://cdn.jsdelivr.net/npm/d3@7/dist/d3.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/topojson-client@3/dist/topojson-client.min.js"></script>

Topology data sources (these require network access):
- US states: https://cdn.jsdelivr.net/npm/us-atlas@3/states-10m.json
- World: https://cdn.jsdelivr.net/npm/world-atlas@2/countries-110m.json

Use CSS variables or __WIDGET_DARK__ check for colors.
`

const visualizeArtDoc = `
## Art and Illustration

Same SVG technical rules (viewBox 680, safe area) but different aesthetic:
- Fill the canvas — art should feel rich, not sparse
- Bold colors: mix categories for variety
- Custom <style> color blocks are fine for art
- Layer overlapping opaque shapes for depth
- Organic forms with <path> curves, <ellipse>, <circle>
- Texture via repetition (parallel lines, dots, hatching) not raster effects
- Geometric patterns with <g transform="rotate()"> for radial symmetry
`
