// Note: You must restart the dev server for changes to take effect

const { readFileSync } = require('fs');
const { basename, dirname, join, relative, resolve } = require('path');
const { env } = require('process');

const { sync } = require('glob');
const rspack = require('@rspack/core');
const extname = require('path-complete-extname');
const { load } = require('js-yaml');
const { RspackManifestPlugin } = require('rspack-manifest-plugin');

// Load Shakapacker configuration
const configFile = env.SHAKAPACKER_CONFIG || env.WEBPACKER_CONFIG || 'config/shakapacker.yml';
const configPath = resolve(configFile);
const currentEnv = env.RAILS_ENV || env.NODE_ENV || 'development';
const settings = load(readFileSync(configPath), 'utf8')[currentEnv];

const themePath = resolve('config', 'themes.yml');
const themes = load(readFileSync(themePath), 'utf8');

const output = {
  path: resolve('public', settings.public_output_path),
  publicPath: `/${settings.public_output_path}/`,
};

// Load rules
const rules = require('./config/webpack/rules');

const extensionGlob = `**/*{${settings.extensions.join(',')}}*`;
const entryPath = join(settings.source_path, settings.source_entry_path);
const packPaths = sync(join(entryPath, extensionGlob));

const baseConfig = {
  entry: Object.assign(
    packPaths.reduce((map, entry) => {
      const localMap = map;
      const namespace = relative(join(entryPath), dirname(entry));
      localMap[join(namespace, basename(entry, extname(entry)))] = resolve(entry);
      return localMap;
    }, {}),
    Object.keys(themes).reduce((themePaths, name) => {
      themePaths[name] = resolve(join(settings.source_path, themes[name]));
      return themePaths;
    }, {}),
  ),

  output: {
    filename: 'js/[name]-[contenthash].js',
    chunkFilename: 'js/[name]-[contenthash].chunk.js',
    hotUpdateChunkFilename: 'js/[id]-[fullhash].hot-update.js',
    hashFunction: 'xxhash64',
    crossOriginLoading: 'anonymous',
    path: output.path,
    publicPath: output.publicPath,
  },

  optimization: {
    runtimeChunk: {
      name: 'common',
    },
    splitChunks: {
      cacheGroups: {
        default: false,
        vendors: false,
        common: {
          name: 'common',
          chunks: 'all',
          minChunks: 2,
          minSize: 0,
          test: /^(?!.*[\\/]node_modules[\\/]react-intl[\\/]).+$/,
        },
      },
    },
  },

  module: {
    rules: Object.keys(rules).map(key => rules[key]),
  },

  plugins: [
    new rspack.EnvironmentPlugin({
      NODE_ENV: env.NODE_ENV || 'development',
      PUBLIC_OUTPUT_PATH: settings.public_output_path,
      CDN_HOST: env.CDN_HOST || '',
      S3_ENABLED: env.S3_ENABLED || 'false',
    }),
    new rspack.CssExtractRspackPlugin({
      filename: 'css/[name]-[contenthash:8].css',
      chunkFilename: 'css/[name]-[contenthash:8].chunk.css',
    }),
    new RspackManifestPlugin({
      fileName: 'manifest.json',
      writeToFileEmit: true,
    }),
  ],

  resolve: {
    extensions: settings.extensions,
    modules: [
      resolve(settings.source_path),
      'node_modules',
    ],
  },

  resolveLoader: {
    modules: ['node_modules'],
  },
};

// Environment-specific configuration
if (env.NODE_ENV === 'development') {
  const watchOptions = {};

  if (env.VAGRANT) {
    watchOptions.poll = 1000;
  }

  module.exports = {
    ...baseConfig,
    mode: 'development',
    cache: true,
    devtool: 'eval-cheap-module-source-map',

    stats: {
      errorDetails: true,
    },

    output: {
      ...baseConfig.output,
      pathinfo: true,
    },

    devServer: {
      client: {
        logging: 'none',
        overlay: false,
      },
      compress: settings.dev_server.compress,
      allowedHosts: 'all',
      host: env.REMOTE_DEV ? '0.0.0.0' : settings.dev_server.host,
      port: settings.dev_server.port,
      server: settings.dev_server.https ? 'https' : 'http',
      hot: settings.dev_server.hmr,
      static: {
        directory: output.path,
      },
      historyApiFallback: {
        disableDotRule: true,
      },
      headers: settings.dev_server.headers,
      setupMiddlewares: (middlewares) => {
        return middlewares;
      },
      watchFiles: {
        options: Object.assign(
          {},
          settings.dev_server.watch_options,
          watchOptions,
        ),
      },
    },
  };
} else if (env.NODE_ENV === 'production') {
  // eslint-disable-next-line import/no-extraneous-dependencies
  const { InjectManifest } = require('@aaroon/workbox-rspack-plugin');
  const { createHash } = require('crypto');

  const root = resolve(__dirname);

  module.exports = {
    ...baseConfig,
    mode: 'production',
    devtool: 'source-map',
    stats: 'normal',
    bail: true,

    optimization: {
      ...baseConfig.optimization,
      minimize: true,
    },

    plugins: [
      ...baseConfig.plugins,
      new InjectManifest({
        additionalManifestEntries: ['1f602.svg', 'sheet_13.png'].map((filename) => {
          const path = resolve(root, 'public', 'emoji', filename);
          const body = readFileSync(path);
          const md5 = createHash('md5');

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
  };
} else {
  module.exports = baseConfig;
}
