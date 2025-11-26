export default function About() {
	return (
		<div className="p-8 max-w-xl mx-auto space-y-3">
			<h1 className="text-2xl font-bold">About</h1>
			<p className="text-gray-700">
				This page is server-rendered, hydrated on the client, and styled via the shared Tailwind
				entry at <code>app.css</code>.
			</p>
			<p className="text-gray-600">Add more pages under <code>cmd/sample/app/pages</code> and run alloy build.</p>
		</div>
	);
}
