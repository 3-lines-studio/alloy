export default function Home() {
	return (
		<div className="min-h-screen bg-slate-950 text-slate-50">
			<div className="absolute inset-0 -z-10 overflow-hidden">
				<div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_20%_20%,rgba(56,189,248,0.06),transparent_30%),radial-gradient(circle_at_80%_10%,rgba(190,24,93,0.05),transparent_25%),radial-gradient(circle_at_70%_70%,rgba(52,211,153,0.06),transparent_25%)]" />
				<div className="absolute inset-x-0 top-0 h-32 bg-linear-to-b from-slate-900 via-slate-950 to-transparent" />
			</div>

			<header className="relative border-b border-white/10 bg-slate-950/70">
				<div className="mx-auto flex max-w-6xl flex-col gap-4 px-6 py-10">
					<div className="flex items-center gap-4">
						<img
							src="/alloy.png"
							alt="Alloy logo"
							className="h-16 w-16 rounded-2xl bg-white/5 ring-1 ring-white/10"
						/>
						<div>
							<h1 className="text-5xl font-semibold leading-tight text-slate-50">Alloy</h1>
							<p className="text-xl font-medium text-slate-50">React SSR for Go. No Node.js runtime. One binary.</p>
						</div>
					</div>
					<p className="max-w-3xl text-lg text-slate-300/90">
						Server-render React components from Go using QuickJS. Build with esbuild and Tailwind. Ship a single static binary.
					</p>
					<div className="flex flex-wrap gap-3 text-sm">
						<a
							href="/01-quick-start"
							className="rounded-full bg-cyan-500 text-slate-950 px-4 py-2 font-semibold shadow-lg shadow-cyan-500/30 transition hover:-translate-y-0.5"
						>
							Quick start
						</a>
						<a
							href="/10-deployment"
							className="rounded-full border border-white/20 px-4 py-2 text-slate-100 transition hover:border-cyan-400/60 hover:text-cyan-100"
						>
							Deployment
						</a>
					</div>
				</div>
			</header>

			<main className="relative mx-auto max-w-6xl px-6 py-12 space-y-10">
				<section className="grid gap-6 md:grid-cols-3">
					{[
						{
							title: 'Go-native SSR',
							desc: 'Render React with QuickJS in-process. Zero Node.js dependencies in production. Pure Go HTTP handlers.',
						},
						{
							title: 'Optional hydration',
							desc: 'Ship static HTML by default. Add client-side interactivity only where needed. Less JavaScript, faster pages.',
						},
						{
							title: 'Single binary deployment',
							desc: 'Prebuilt assets via alloy CLI. Embed with go:embed. Deploy one executable—no Node runtime required.',
						},
					].map((item) => (
						<div
							key={item.title}
							className="rounded-2xl border border-white/10 bg-slate-900/70 p-5 shadow-lg shadow-slate-900/50"
						>
							<h3 className="text-lg font-semibold text-slate-50">{item.title}</h3>
							<p className="mt-2 text-sm text-slate-300/90">{item.desc}</p>
						</div>
					))}
				</section>

				<section className="rounded-2xl border border-white/10 bg-slate-900/80 p-8 shadow-xl shadow-cyan-500/10">
					<h2 className="text-2xl font-semibold text-slate-50">Why Alloy</h2>
					<div className="mt-6 grid gap-4 md:grid-cols-3 text-sm">
						<div>
							<p className="font-medium text-slate-200">vs Next.js</p>
							<p className="mt-1 text-slate-400">No Node.js in production. Simpler deployment—one binary instead of complex infrastructure.</p>
						</div>
						<div>
							<p className="font-medium text-slate-200">vs templ</p>
							<p className="mt-1 text-slate-400">Full React/TypeScript ecosystem. Client-side hydration for interactive components.</p>
						</div>
						<div>
							<p className="font-medium text-slate-200">vs htmx</p>
							<p className="mt-1 text-slate-400">Component-based architecture. Modern build tooling. Type-safe props.</p>
						</div>
					</div>
				</section>

				<section className="rounded-2xl border border-white/10 bg-slate-900/80 p-8 shadow-xl">
					<h2 className="text-2xl font-semibold text-slate-50">Three steps to production</h2>
					<div className="mt-6 grid gap-6 md:grid-cols-3">
						<div>
							<p className="text-xs font-semibold uppercase tracking-wider text-cyan-300">1. Define page</p>
							<pre className="mt-3 overflow-x-auto rounded-lg bg-slate-950 p-4 text-xs text-slate-300">
{`// app/pages/home.tsx
export default function Home({ title }) {
  return <h1>{title}</h1>
}`}
							</pre>
						</div>
						<div>
							<p className="text-xs font-semibold uppercase tracking-wider text-cyan-300">2. Props loader</p>
							<pre className="mt-3 overflow-x-auto rounded-lg bg-slate-950 p-4 text-xs text-slate-300">
{`// loader/home.go
func Home(r *http.Request) map[string]any {
  return map[string]any{"title": "Hello"}
}`}
							</pre>
						</div>
						<div>
							<p className="text-xs font-semibold uppercase tracking-wider text-cyan-300">3. Server setup</p>
							<pre className="mt-3 overflow-x-auto rounded-lg bg-slate-950 p-4 text-xs text-slate-300">
{`pages := []alloy.Page{{
  Route: "/", Component: "app/pages/home.tsx", Props: loader.Home,
}}
alloy.ListenAndServe(":8080", embeddedDist, pages)`}
							</pre>
						</div>
					</div>
					<p className="mt-6 text-sm text-slate-400">
						Build with <code className="rounded bg-slate-800 px-1.5 py-0.5 font-mono text-cyan-300">alloy</code> CLI, embed assets with <code className="rounded bg-slate-800 px-1.5 py-0.5 font-mono text-cyan-300">go:embed</code>, deploy one binary.
					</p>
				</section>
			</main>
		</div>
	);
}
