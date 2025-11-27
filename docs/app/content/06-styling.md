# Styling

Style alloy apps with Tailwind CSS v4.

## Setup

### Install Tailwind

```sh
npm install tailwindcss
```

### Create CSS file

Create `app/pages/app.css`:

```css
@import "tailwindcss";
```

### Build

```sh
alloy
```

Outputs `app/dist/alloy/shared.css` with compiled Tailwind.

## Usage in components

### Utility classes

```tsx
export default function Home({ title }) {
	return (
		<div className="min-h-screen bg-slate-950 text-white">
			<header className="border-b border-white/10 p-6">
				<h1 className="text-4xl font-bold">{title}</h1>
			</header>
			<main className="max-w-6xl mx-auto p-6">
				<p className="text-lg text-slate-300">Content here</p>
			</main>
		</div>
	);
}
```

### Responsive design

```tsx
<div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-6">
	<div className="rounded-lg border border-white/10 p-4">Card 1</div>
	<div className="rounded-lg border border-white/10 p-4">Card 2</div>
	<div className="rounded-lg border border-white/10 p-4">Card 3</div>
</div>
```

## Custom styles

Add custom CSS to `app.css`:

```css
@import "tailwindcss";

.custom-gradient {
	background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
}

.prose {
	@apply text-slate-800 leading-relaxed;
}

.prose h1 {
	@apply text-4xl font-bold mb-4;
}
```

Use in components:

```tsx
<div className="custom-gradient p-8">
	<h1>Gradient background</h1>
</div>
```

## Tailwind v4 features

### CSS variables

```css
@import "tailwindcss";

@theme {
	--color-brand: #3b82f6;
	--font-display: "Inter", sans-serif;
}
```

```tsx
<h1 className="text-[--color-brand] font-[--font-display]">
	Styled with variables
</h1>
```

### No config file

Tailwind v4 doesn't require `tailwind.config.js`. Configuration is CSS-based.

## Component styling

### Reusable components

```tsx
// app/components/Button.tsx
export function Button({ children, variant = 'primary' }) {
	const baseClasses = 'px-4 py-2 rounded-lg font-medium transition';
	const variants = {
		primary: 'bg-blue-500 text-white hover:bg-blue-600',
		secondary: 'bg-slate-200 text-slate-900 hover:bg-slate-300',
	};

	return (
		<button className={`${baseClasses} ${variants[variant]}`}>
			{children}
		</button>
	);
}
```

```tsx
import { Button } from '../components/Button';

export default function Page() {
	return (
		<div>
			<Button variant="primary">Click me</Button>
			<Button variant="secondary">Cancel</Button>
		</div>
	);
}
```

## Dark mode

```css
@import "tailwindcss";

@media (prefers-color-scheme: dark) {
	:root {
		--background: #0f172a;
		--text: #f1f5f9;
	}
}
```

```tsx
<div className="bg-white dark:bg-slate-900 text-slate-900 dark:text-white">
	<p>Adapts to system theme</p>
</div>
```

## Production optimization

The `alloy` CLI automatically:
- Purges unused CSS
- Minifies output
- Generates single `shared.css` for all pages

No additional configuration needed.

## Fonts

### Web fonts

```css
@import "tailwindcss";
@import url('https://fonts.googleapis.com/css2?family=Inter:wght@400;600;700&display=swap');

@theme {
	--font-sans: "Inter", sans-serif;
}
```

### Self-hosted fonts

Place fonts in `public/fonts/`:

```css
@import "tailwindcss";

@font-face {
	font-family: 'CustomFont';
	src: url('/fonts/custom.woff2') format('woff2');
	font-weight: 400;
	font-display: swap;
}

@theme {
	--font-custom: "CustomFont", sans-serif;
}
```

## Next steps

- [Project structure](/02-project-structure) - File organization
- [Production builds](/09-production-builds) - CSS compilation
- [Client hydration](/05-client-hydration) - Interactive components
