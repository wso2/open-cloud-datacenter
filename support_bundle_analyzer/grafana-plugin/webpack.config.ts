import CopyWebpackPlugin from 'copy-webpack-plugin';
import path from 'path';
import type { Configuration } from 'webpack';

import grafanaConfig, { type Env } from './.config/webpack/webpack.config';

const config = async (env: Env): Promise<Configuration> => {
  const baseConfig = await grafanaConfig(env);

  return {
    ...baseConfig,
    plugins: [
      ...(baseConfig.plugins ?? []),
      new CopyWebpackPlugin({
        patterns: [
          {
            from: path.resolve(process.cwd(), 'rules', 'rca-rules.yaml'),
            to: 'rules/rca-rules.yaml',
            force: true,
          },
        ],
      }),
    ],
  };
};

export default config;
