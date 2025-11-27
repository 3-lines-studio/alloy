# Client Hydration

Add interactivity to server-rendered pages by selectively hydrating components.

## Overview

**Server-side rendering** produces static HTML. **Client-side hydration** attaches event listeners and state management.

Alloy's approach: **optional hydration**. Components are static by default. Only components using React hooks (useState, useEffect, etc.) are hydrated.

## When hydration happens

### Automatic detection

Alloy analyzes your component code. If it contains hooks, hydration is enabled.

**Static component (no hydration):**

```tsx
export default function About() {
	return <div>Static content</div>;
}
```

No JavaScript shipped to client. Pure HTML.

**Interactive component (hydrated):**

```tsx
import { useState } from 'react';

export default function Counter({ initial }) {
	const [count, setCount] = useState(initial);

	return (
		<div>
			<p>Count: {count}</p>
			<button onClick={() => setCount(count + 1)}>Increment</button>
		</div>
	);
}
```

Client bundle included. `onClick` handler works.

### Hooks that trigger hydration

- `useState`
- `useEffect`
- `useReducer`
- `useContext`
- `useRef`
- `useCallback`
- `useMemo`
- `useLayoutEffect`
- Custom hooks using the above

## Hydration process

1. Server renders component to HTML
2. HTML sent to browser with embedded props
3. Client bundle downloads
4. React hydrates: attaches event listeners to existing DOM
5. Component becomes interactive

## Mixed static/interactive pages

Combine static and interactive components on the same page:

```tsx
import { Header } from '../components/Header';  // Static
import { Counter } from '../components/Counter';  // Interactive

export default function Home({ title, count }) {
	return (
		<div>
			<Header title={title} />  {/* No JavaScript */}
			<Counter initial={count} />  {/* Hydrated */}
		</div>
	);
}
```

Only `Counter` gets hydrated. `Header` remains static HTML.

## Islands architecture

This pattern is called **islands architecture**: interactive components ("islands") in a sea of static content.

Benefits:
- Less JavaScript shipped
- Faster page loads
- Better performance on slow devices

## Client bundle generation

Alloy generates client bundles with esbuild:

```
app/dist/alloy/
├── home-client.js      # Hydration code for home.tsx
├── counter-client.js   # Hydration code for components with hooks
└── chunks/             # Shared dependencies
```

**Code splitting:** Shared dependencies extracted to separate chunks. Multiple pages using React only download it once.

## Hydration script

The HTML includes a hydration script:

```html
<div id="home-root">
  <!-- Server-rendered HTML -->
</div>

<script>
  window.__ALLOY_PROPS__ = {"title":"Hello","count":0};
</script>
<script src="/app/dist/alloy/home-client.js"></script>
```

Client bundle reads `__ALLOY_PROPS__`, hydrates the component.

## Common patterns

### Form submission

```tsx
import { useState } from 'react';

export default function ContactForm() {
	const [name, setName] = useState('');
	const [submitted, setSubmitted] = useState(false);

	const handleSubmit = async (e) => {
		e.preventDefault();
		await fetch('/api/contact', {
			method: 'POST',
			body: JSON.stringify({ name }),
			headers: { 'Content-Type': 'application/json' },
		});
		setSubmitted(true);
	};

	if (submitted) {
		return <p>Thank you!</p>;
	}

	return (
		<form onSubmit={handleSubmit}>
			<input
				value={name}
				onChange={(e) => setName(e.target.value)}
				placeholder="Your name"
			/>
			<button type="submit">Submit</button>
		</form>
	);
}
```

**Server renders** the initial form.

**Client hydrates** to handle input changes and submission.

### Data fetching on client

```tsx
import { useState, useEffect } from 'react';

export default function LiveData({ initialData }) {
	const [data, setData] = useState(initialData);

	useEffect(() => {
		const interval = setInterval(async () => {
			const res = await fetch('/api/data');
			const json = await res.json();
			setData(json);
		}, 5000);

		return () => clearInterval(interval);
	}, []);

	return <div>Data: {JSON.stringify(data)}</div>;
}
```

**Server provides** `initialData` for fast first paint.

**Client polls** `/api/data` every 5 seconds.

### Conditional interactivity

Show static content first, hydrate on demand:

```tsx
import { useState } from 'react';

export default function Comments({ commentsHTML }) {
	const [showForm, setShowForm] = useState(false);

	return (
		<div>
			<div dangerouslySetInnerHTML={{__html: commentsHTML}} />
			{showForm ? (
				<CommentForm />
			) : (
				<button onClick={() => setShowForm(true)}>
					Add comment
				</button>
			)}
		</div>
	);
}
```

Form only renders when user clicks button. Saves initial bundle size.

## Hydration mismatch errors

If server HTML doesn't match client render, React logs warnings:

```
Warning: Text content did not match. Server: "Hello" Client: "Goodbye"
```

**Causes:**
- Different props between server and client
- Randomness (Math.random(), Date.now()) in component
- Browser-specific APIs (window, navigator) in render

**Fix:**
Use consistent props and avoid side effects in render.

**Example issue:**

```tsx
export default function Time() {
	const now = new Date().toISOString();
	return <p>{now}</p>;
}
```

Server renders at time T1, client hydrates at time T2. Mismatch.

**Fix:** Pass timestamp as prop:

```go
func Time(r *http.Request) map[string]any {
	return map[string]any{"now": time.Now().Format(time.RFC3339)}
}
```

```tsx
export default function Time({ now }) {
	return <p>{now}</p>;
}
```

## Performance tips

### Lazy hydration

Defer hydration until component is visible:

```tsx
import { useState, useEffect, useRef } from 'react';

export default function LazyWidget({ data }) {
	const [hydrated, setHydrated] = useState(false);
	const ref = useRef(null);

	useEffect(() => {
		const observer = new IntersectionObserver((entries) => {
			if (entries[0].isIntersecting) {
				setHydrated(true);
			}
		});

		if (ref.current) {
			observer.observe(ref.current);
		}

		return () => observer.disconnect();
	}, []);

	if (!hydrated) {
		return <div ref={ref}>Loading...</div>;
	}

	return <InteractiveComponent data={data} />;
}
```

Widget only hydrates when scrolled into view.

### Minimize bundle size

- Avoid large dependencies (lodash, moment.js)
- Use native APIs where possible
- Tree-shake unused imports

### Prefetch bundles

Add prefetch hints for faster interactivity:

```html
<link rel="prefetch" href="/app/dist/alloy/home-client.js">
```

Browser downloads bundle early, before user interacts.

## Debugging hydration

### Check bundle loading

Open DevTools Network tab:
- Verify `*-client.js` loads
- Check for 404s (wrong paths in manifest)

### Inspect props

Check `window.__ALLOY_PROPS__` in browser console:

```js
console.log(window.__ALLOY_PROPS__);
```

Should match props from your loader function.

### React DevTools

Install [React DevTools](https://react.dev/learn/react-developer-tools) browser extension.

Inspect component tree, props, and state.

## Static export (no hydration)

To completely disable hydration and ship zero client JavaScript, don't use hooks:

```tsx
export default function Static({ content }) {
	return <div>{content}</div>;
}
```

No client bundle generated. Pure static HTML.

## Next steps

- [Server rendering](/04-server-rendering) - How SSR works
- [Dynamic data](/07-dynamic-data) - Data fetching
- [Performance](/17-performance) - Optimization techniques
