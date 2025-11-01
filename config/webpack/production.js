// Note: You must restart bin/webpack-dev-server for changes to take effect

/* eslint-disable import/no-extraneous-dependencies */

const { createHash } = require('crypto');
const { readFileSync } = require('fs');
const { resolve } = require('path');

const { InjectManifest } = require('@aaroon/workbox-rspack-plugin');
const TerserPlugin = require('terser-webpack-plugin');
const { BundleAnalyzerPlugin } = require('webpack-bundle-analyzer');
const { merge } = require('webpack-merge');

const sharedConfig = require('./shared');

const root = resolve(__dirname, '..', '..');

module.exports = merge(sharedConfig, {
  mode: 'production',
  devtool: 'source-map',
  stats: 'normal',
  bail: true,
  optimization: {
    minimize: true,
    minimizer: [
      new TerserPlugin({
        parallel: true,
        terserOptions: {
          compress: {
            warnings: false,
          },
        },
      }),
    ],
  },

  plugins: [
    new BundleAnalyzerPlugin({ // generates report.html
      analyzerMode: 'static',
      openAnalyzer: false,
      logLevel: 'silent', // keep Shakapacker quiet when running with --json
    }),
    new InjectManifest({
      additionalManifestEntries: ['1f602.svg', 'sheet_13.png'].map((filename) => {
        const path = resolve(root, 'public', 'emoji', filename);
        const body = readFileSync(path);
        const md5  = createHash('md5');

        md5.update(body);

        return {
          revision: md5.digest('hex'),
          url: `/emoji/${filename}`,
        };
      }),
      exclude: [
        /(?:base|extra)_polyfills-.*\.js$/,
        /locale_.*\.js$/,
        /mailer-.*\.(?:css|js)$/,
      ],
      include: [/\.js$/, /\.css$/],
      maximumFileSizeToCacheInBytes: 2 * 1_024 * 1_024, // 2 MiB
      swDest: resolve(root, 'public', 'packs', 'sw.js'),
      swSrc: resolve(root, 'app', 'javascript', 'mastodon', 'service_worker', 'entry.js'),
    }),
  ],
});
