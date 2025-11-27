type DocsProps = {
	slug: string;
	html: string;
	entries?: { slug: string; title: string }[];
	title?: string;
};

export default function Docs({ slug, html, entries = [], title }: DocsProps) {
	return (
		<div className="min-h-screen">
			<div className="absolute inset-0 -z-10 overflow-hidden">
				<div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_20%_20%,rgba(56,189,248,0.06),transparent_30%),radial-gradient(circle_at_80%_10%,rgba(190,24,93,0.05),transparent_25%),radial-gradient(circle_at_70%_70%,rgba(52,211,153,0.06),transparent_25%)]" />
				<div className="absolute inset-x-0 top-0 h-32 bg-linear-to-b from-slate-900 via-slate-950 to-transparent" />
			</div>

			<header className="relative border-b border-white/10 bg-slate-950/70">
				<div className="mx-auto flex max-w-6xl flex-col gap-3 px-6 py-8">
					<p className="text-xs uppercase tracking-[0.35em] text-slate-400">Alloy Docs</p>
					<h1 className="text-4xl font-semibold leading-tight text-slate-50">Built with the Alloy renderer.</h1>
					<p className="max-w-3xl text-base text-slate-300">
						Markdown is converted to HTML on the Go server with Goldmark, then rendered through this TSX page. The sidebar
						lists every markdown file under <code className="mx-1 rounded bg-slate-800 px-1.5 py-0.5 text-xs text-amber-200">app/content</code>, giving you a manifest-backed docs set.
					</p>
				</div>
			</header>

			<main className="relative mx-auto flex max-w-6xl gap-6 px-6 py-10">
				<aside className="sticky top-10 h-fit w-64 shrink-0 space-y-3 rounded-2xl border border-white/10 bg-slate-900/70 p-4 shadow-lg shadow-slate-900/40">
					<p className="text-xs font-semibold uppercase tracking-[0.2em] text-cyan-200">Docs</p>
					<nav className="space-y-1 text-sm text-slate-200/90">
						{entries.map((entry) => {
							const isActive = entry.slug === slug;
							return (
								<a
									key={entry.slug}
									href={`/${entry.slug}`}
									className={`flex items-center justify-between rounded-lg px-3 py-2 transition ${
										isActive
											? 'bg-cyan-500/15 text-cyan-100 ring-1 ring-cyan-400/40'
											: 'hover:bg-slate-800/70 hover:text-slate-50'
									}`}
								>
									<span className="truncate">{entry.title}</span>
								</a>
							);
						})}
					</nav>
				</aside>

				<section className="flex-1 space-y-4">
					<div className="rounded-2xl border border-white/10 bg-slate-900/80 p-6 shadow-xl shadow-slate-900/60">
						<div
							className="prose prose-invert max-w-none prose-headings:text-slate-50 prose-p:text-slate-200 prose-li:text-slate-200 prose-strong:text-emerald-200"
							dangerouslySetInnerHTML={{ __html: html }}
						/>
					</div>
				</section>
			</main>
		</div>
	);
}
