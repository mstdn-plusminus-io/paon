// Common configuration for Shakapacker loaded from config/shakapacker.yml

const { readFileSync } = require('fs');
const { resolve } = require('path');
const { env } = require('process');

const { load } = require('js-yaml');

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

module.exports = {
  settings,
  themes,
  env: {
    NODE_ENV: env.NODE_ENV,
    PUBLIC_OUTPUT_PATH: settings.public_output_path,
  },
  output,
};
