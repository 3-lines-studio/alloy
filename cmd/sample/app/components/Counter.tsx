import { useState } from 'react';

type CounterProps = {
	initial?: number;
};

export function Counter({ initial = 0 }: CounterProps) {
	const [count, setCount] = useState(initial);

	return (
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
	);
}
