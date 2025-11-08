// Note: You must restart bin/webpack-dev-server for changes to take effect

const { env } = require('process');

// Load environment-specific configuration
const environment = env.NODE_ENV || env.RAILS_ENV || 'development';

let config;
if (environment === 'production') {
  config = require('./production');
} else if (environment === 'test') {
  config = require('./tests');
} else {
  config = require('./development');
}

module.exports = config;
