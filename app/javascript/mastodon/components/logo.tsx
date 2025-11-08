import logoSymbolWordmark from 'mastodon/../images/logo-symbol-wordmark.svg';
import logo from 'mastodon/../images/logo.svg';

export const WordmarkLogo: React.FC = () => (
  <img src={logoSymbolWordmark} alt='Mastodon' className='logo logo--icon' />
);

export const SymbolLogo: React.FC = () => (
  <img src={logo} alt='Mastodon' className='logo logo--icon' />
);
