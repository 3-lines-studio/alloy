import { useState } from 'react';

type Meta = {
	title?: string;
	description?: string;
	url?: string;
	canonical?: string;
	image?: string;
	ogType?: string;
};

export default function Home({
	title,
	items,
	timestamp,
	slug,
	meta,
}: {
	title: string;
	items: string[];
	timestamp: string;
	slug?: string;
	meta?: Meta;
}) {
	const [count, setCount] = useState(0);

	return (
		<div className="p-8 max-w-2xl mx-auto space-y-6">
			<header className="space-y-2">
				<p className="text-xs uppercase tracking-wide text-gray-500">Alloy</p>
				<h1 className="text-3xl font-bold">
					{title} - {timestamp}
				</h1>
				{slug ? <p className="text-sm text-gray-600">Slug: {slug}</p> : null}
				{meta?.description ? <p className="text-gray-600">{meta.description}</p> : null}
			</header>

			<section className="p-4 bg-gray-50 rounded border border-gray-200 space-y-3">
				<p className="text-sm text-gray-600">Counter lives on the client.</p>
				<div className="flex items-center gap-3">
					<button
						onClick={() => setCount(count - 1)}
						className="px-3 py-2 bg-gray-200 rounded hover:bg-gray-300"
					>
						-
					</button>
					<span className="text-lg font-semibold min-w-12 text-center">{count}</span>
					<button
						onClick={() => setCount(count + 1)}
						className="px-3 py-2 bg-blue-600 text-white rounded hover:bg-blue-700"
					>
						+
					</button>
				</div>
			</section>

			<section className="space-y-2">
				<h2 className="text-lg font-semibold">Items</h2>
				<ul className="list-disc list-inside space-y-1">
					{items.map((item, idx) => (
						<li key={idx} className="text-gray-700">
							{item}
						</li>
					))}
				</ul>
			</section>
		</div>
	);
}
