# CLAUDE.md - Project Context & Coding Guidelines

## Overview
This repository contains a **Go 1.26** web server delivering high-performance, lightweight web applications built with **Modern Native JavaScript (ES2026)**, **Semantic HTML5**, and **Tailwind CSS**.

---

## Language & Runtime Specifications
- **Go**: Version 1.26 (Standard library focused, strict idiomatic Go)
- **JavaScript**: ES2026 Native Specification (No heavy frameworks/transpilers)
- **HTML**: HTML5 Semantic Standard
- **CSS**: Tailwind CSS

---

## Code Philosophy & Core Principles
1. **Clean & Self-Explanatory**: Code structure and variable naming must speak for themselves. Avoid obscure abbreviations or overly verbose wrappers.
2. **Zero Redundancy**: Eliminate duplicate logic across all layers. Reuse utility functions and standard HTTP handlers cleanly.
3. **Optimized Execution**: Code must be written with high throughput, minimal allocations, and low overhead in mind.
4. **Up-to-Date Language Features**: Utilize modern idiomatic patterns introduced in Go 1.26 and modern JavaScript standard APIs (ES2026).
5. **Documentation Standard**:
   - Comments must describe **only** what the current function/type does or inline business logic explanations for dev better understanding.
   - **NO** history, changelogs, LLM explanations, inline logic commentary, or decision rationales.
   - Doc comments must adhere to official language conventions (e.g., Godoc for Go, JSDoc for JS).

---

## Go (1.26) Guidelines

### HTTP Server & Routing
- Use the standard library HTTP router (`net/http.ServeMux`) with standard enhanced routing pattern support (`METHOD /path`).
- Leverage Go's `embed` package (`embed.FS`) to bundle static assets (`.html`, `.js`, `.css`) into the binary for deployment.
- Enforce strict server timeouts (`ReadTimeout`, `WriteTimeout`, `IdleTimeout`) on `http.Server`.

### Concurrency & Performance
- Handle request cancellations and timeouts using `context.Context`.
- Avoid unnecessary dynamic allocations; prefer pre-allocating slices (`make([]T, 0, capacity)`) when size is predictable.
- Use explicit error handling—never ignore returned errors. Wrap errors with context where necessary (`fmt.Errorf("...: %w", err)`).

### Naming & Structure
- Package names must be lowercase, single-word entities (e.g., `server`, `handler`).
- Variable and function names should be short, concise, and self-documenting.
- Follow Godoc format: start comments with the name of the declared item.

---

## Native JavaScript (ES2026) Guidelines

### Language Standards
- **No Build Tools/Frameworks**: Rely on modern browser standards (`fetch`, `CustomElements`, modern DOM APIs).
- **Modules**: Always use native ES Modules (`<script type="module" src="...">`).
- **Features**: Use strict modern features such as optional chaining (`?.`), nullish coalescing (`??`), logical assignment (`||=`, `&&=`), `Object.hasOwn()`, `Promise.withResolvers()`, and modern Array/Collection methods (`.toSorted()`, `.toReversed()`, `.toSpliced()`, `Map.groupBy()`).

### Code Structure & Async
- Always mark main entry points as `'use strict';` inside module scope where applicable.
- Use `async/await` for asynchronous control flows with `try/catch` or modern Promise handling. Avoid callback chains.
- Prefer Event Delegation over attaching event listeners to multiple DOM nodes individually.

### Naming & JSDoc
- `camelCase` for variables and function names.
- `PascalCase` for classes and Custom Elements.
- Comments must strictly follow JSDoc and specify parameters, return types, and high-level behavioral description.

---

## HTML5 & Tailwind CSS Guidelines

### HTML Structure
- Use semantic structural tags (`<header>`, `<main>`, `<nav>`, `<article>`, `<section>`, `<footer>`).
- Ensure accessible markup: include explicit `aria-*` attributes where semantics fall short, proper `label` associations, and valid `alt` text.
- Include proper Meta tags for response viewport sizing and charset (`<meta charset="UTF-8">`, `<meta name="viewport" content="width=device-width, initial-scale=1.0">`).

### Tailwind CSS Conventions
- Group utility classes logically:
  1. Layout & Display (`flex`, `grid`, `block`, `hidden`)
  2. Positioning (`relative`, `absolute`, `top-0`)
  3. Sizing (`w-full`, `max-w-md`, `h-12`)
  4. Typography (`text-base`, `font-semibold`, `text-slate-900`)
  5. Spacing (`p-4`, `m-2`, `gap-4`)
  6. Visuals & Interactive (`bg-white`, `rounded-lg`, `shadow-sm`, `hover:`, `focus:`, `dark:`)
- Avoid arbitrary Tailwind values (`w-[357px]`) unless strictly required. Use default scale parameters.
- Keep HTML clean by using native class aggregation or standard reusable utility classes for repeated visual components.

### Dark Mode
- This project is dark-mode-aware via `darkMode: 'class'` (a `.dark` class toggled on `<html>`) and semantic color tokens defined in `admin/static/css/theme.css`, exposed to Tailwind as `canvas`, `surface`, `surface-hover`, `surface-active`, `surface-strong`, `line`, `line-strong`, `fg`, `fg-muted`, `fg-subtle`.
- Always color new markup with these semantic classes (`bg-canvas`, `bg-surface`, `text-fg`, `text-fg-muted`, `border-line`, etc.) instead of raw Tailwind grays/slates (`bg-white`, `text-gray-900`, `border-gray-200`). This makes the element dark-mode-correct automatically, with no `dark:` variant needed.
- Only reach for an explicit `dark:` variant when a color is intentionally *not* one of the semantic tokens (e.g. a brand/status hue like `emerald`, a chart line color). In that case define both the light and dark shade explicitly, and preserve the same relative meaning (ordering, hue identity, contrast) across themes rather than relying on opacity tricks (e.g. `/20`) to fake a shade.
- `<input>`, `<select>`, and `<textarea>` already get a themed background/foreground globally from `theme.css` — don't add per-element `bg-*`/`dark:bg-*` classes to individual form controls.
- Elements meant to be interactive controls (buttons, toggles) must look distinct from plain-text nav/inline links: give them a visible `border` and `bg-surface`/`bg-surface-hover`, not just muted text color.
- Colors painted via JS outside of Tailwind classes (inline `style`, SVG attributes like `stroke`/`fill`) are invisible to `dark:` variants and must be computed at render/paint time from the current theme (see `isDarkTheme()` in `app.js`), and recomputed on the `themechange` event dispatched on `window` so they update live without a page reload.

---

## Instructions for AI / LLM
- Follow **all** patterns defined in this document without exception.
- Prioritize native execution capabilities over external third-party dependencies.
- **NEVER** write commentary or commit explanations in comments (e.g., do NOT write `// Added error check as requested by LLM` or `// Updated for better performance`). Write only clean, clean function behavior comments.
- Keep variable names clean, expressive, and concise.
