import { hydrateRoot } from 'react-dom/client';
import Component from '%s';

const propsEl = document.getElementById('%s-props');
const props = propsEl ? JSON.parse(propsEl.textContent || '{}') : {};
const rootEl = document.getElementById('%s');

if (rootEl) {
	hydrateRoot(rootEl, <Component {...props} />);
}
