// Note: You must restart dev server for changes to take effect

const { env } = require('process');

// Load environment-specific configuration from webpack directory
// Rspack is compatible with webpack configurations
const environment = env.NODE_ENV || env.RAILS_ENV || 'development';

let config;
if (environment === 'production') {
  config = require('../webpack/production');
} else if (environment === 'test') {
  config = require('../webpack/tests');
} else {
  config = require('../webpack/development');
}

module.exports = config;
