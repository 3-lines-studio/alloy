import { Counter } from '../components/Counter';

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

			<Counter />

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
