const rspack = require('@rspack/core');

module.exports = {
  test: /\.s?css$/i,
  use: [
    rspack.CssExtractRspackPlugin.loader,
    {
      loader: 'css-loader',
      options: {
        sourceMap: true,
        importLoaders: 2,
      },
    },
    {
      loader: 'postcss-loader',
      options: {
        sourceMap: true,
      },
    },
    {
      loader: 'sass-loader',
      options: {
        implementation: require('sass'),
        sourceMap: true,
      },
    },
  ],
};
