const { grafanaESModules, nodeModulesToTransform } = require('./jest/utils');

module.exports = {
  testEnvironment: 'jsdom',
  testMatch: ['<rootDir>/src/**/*.test.(ts|tsx)'],
  transform: {
    '^.+\\.[jt]sx?$': [
      '@swc/jest',
      {
        jsc: {
          parser: {
            syntax: 'typescript',
            tsx: true,
          },
          transform: {
            react: {
              runtime: 'automatic',
            },
          },
        },
      },
    ],
  },
  moduleNameMapper: {
    '^components/(.*)$': '<rootDir>/src/components/$1',
    '^react-inlinesvg$': '<rootDir>/.config/jest/mocks/react-inlinesvg.tsx',
    '\\.(css|scss|sass)$': 'identity-obj-proxy',
  },
  transformIgnorePatterns: [nodeModulesToTransform(grafanaESModules)],
  setupFilesAfterEnv: ['<rootDir>/jest-setup.js'],
};
