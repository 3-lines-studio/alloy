type Meta = {
	title?: string;
	description?: string;
	url?: string;
	canonical?: string;
	image?: string;
	ogType?: string;
};

export default function Product({
	title,
	store,
	product,
	timestamp,
	availability,
	price,
	meta,
}: {
	title: string;
	store?: string;
	product?: string;
	timestamp: string;
	availability: string;
	price: string;
	meta?: Meta;
}) {
	return (
		<div className="p-8 max-w-2xl mx-auto space-y-6">
			<header className="space-y-2">
				<p className="text-xs uppercase tracking-wide text-gray-500">Alloy Store</p>
				<h1 className="text-3xl font-bold">{title}</h1>
				{product ? <p className="text-sm text-gray-600">Product slug: {product}</p> : null}
				{store ? <p className="text-sm text-gray-600">Store slug: {store}</p> : null}
				<p className="text-xs text-gray-500">Rendered at {timestamp}</p>
				{meta?.description ? <p className="text-gray-600">{meta.description}</p> : null}
			</header>

			<section className="p-4 bg-white border border-gray-200 rounded space-y-2 shadow-sm">
				<div className="flex items-center justify-between">
					<span className="text-lg font-semibold">Price</span>
					<span className="text-xl font-bold text-green-700">{price}</span>
				</div>
				<div className="flex items-center justify-between">
					<span className="text-lg font-semibold">Availability</span>
					<span className="text-sm text-gray-700">{availability}</span>
				</div>
			</section>
		</div>
	);
}
