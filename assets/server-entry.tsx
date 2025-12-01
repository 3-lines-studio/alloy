import { renderToString } from 'react-dom/server.edge';
import Component from '%s';

export default function render(props: any) {
	return renderToString(<Component {...props} />);
}
