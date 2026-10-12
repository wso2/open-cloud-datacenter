import { TextDecoder, TextEncoder } from 'node:util';
import '@testing-library/jest-dom';

Object.assign(globalThis, { TextDecoder, TextEncoder });

Object.defineProperty(globalThis, 'matchMedia', {
	writable: true,
	value: jest.fn((media) => ({
		matches: false,
		media,
		onchange: null,
		addListener: jest.fn(),
		removeListener: jest.fn(),
		addEventListener: jest.fn(),
		removeEventListener: jest.fn(),
		dispatchEvent: jest.fn(),
	})),
});
