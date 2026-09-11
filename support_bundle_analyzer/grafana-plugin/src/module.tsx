import { AppPlugin } from '@grafana/data';
import { ConfigPage } from './pages/ConfigPage';
import { RcaPage } from './pages/RcaPage';

export const plugin = new AppPlugin<{}>().setRootPage(RcaPage).addConfigPage({
  title: 'Configuration',
  icon: 'cog',
  body: ConfigPage,
  id: 'config',
});
