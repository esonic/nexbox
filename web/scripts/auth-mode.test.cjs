const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const ts = require('typescript');

// Render the real routing/settings components with a minimal hook runtime.
// These checks do not require a browser, a login cookie, or a model service.
function component(file, name, response) {
  const states = [];
  let index = 0;
  let mounted = false;
  const effects = [];
  const react = {
    useState(initial) {
      const i = index++;
      if (!(i in states)) states[i] = initial;
      return [states[i], value => { states[i] = value; }];
    },
    useEffect(effect) { if (!mounted) effects.push(effect); },
  };
  const authApi = { me: async () => {
    if (response instanceof Error) throw response;
    return response;
  } };
  const jsx = (type, props) => ({ type, props });
  const source = fs.readFileSync(path.join(__dirname, '../src', file), 'utf8');
  const compiled = ts.transpileModule(source + `\nexport { ${name} as TestedComponent };`, {
    compilerOptions: { jsx: ts.JsxEmit.ReactJSX, module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  const exports = {};
  vm.runInNewContext(compiled, {
    exports,
    require(id) {
      if (id === 'react') return react;
      if (id === 'react/jsx-runtime') return { jsx, jsxs: jsx };
      if (id === 'react-router-dom') return { Routes: 'Routes', Route: 'Route', Navigate: 'Navigate', useLocation: () => ({ pathname: '/projects', search: '' }) };
      if (id === '@tanstack/react-query') return { QueryClient: class {} };
      if (id === '@/lib/api') return { authApi, serverApi: {
        availableAgents: async () => ({ agents: [], default: 'claude' }),
        getSettings: async () => ({ chrome_headless: true }),
      } };
      if (id === '@/stores/preferences') return { CHAT_FONT_SIZE_LABELS: { sm: 'Small', md: 'Medium', lg: 'Large', xl: 'Extra large' }, usePreferences: select => select({ chatFontSize: 'md', setChatFontSize() {} }) };
      return new Proxy({}, { get: (_, key) => key });
    },
  });
  return async () => {
    index = 0;
    exports.TestedComponent();
    mounted = true;
    effects.forEach(effect => effect());
    await new Promise(setImmediate);
    index = 0;
    return exports.TestedComponent();
  };
}

for (const local of [true, false]) {
  test(`${local ? 'local' : 'signed-in'} mode enters the workspace`, async () => {
    const status = local ? { auth_required: false } : { auth_required: true, username: 'owner', user_id: 2 };
    const tree = await component('App.tsx', 'AppRoutes', status)();
    const routes = tree.props.children;
    assert.equal(routes.find(route => route.props.path === '/*').props.element.type.name, 'MainLayout');
    assert.equal(routes.find(route => route.props.path === '/login').props.element.type, local ? 'Navigate' : 'LoginPage');
  });

  test(`${local ? 'local mode hides' : 'signed-in mode shows'} account controls`, async () => {
    const status = local ? { auth_required: false } : { auth_required: true, username: 'owner', user_id: 2 };
    const tree = await component('pages/settings/GeneralSettings.tsx', 'GeneralSettings', status)();
    const rendered = JSON.stringify(tree);
    assert.equal(rendered.includes('Signed in as'), !local);
    assert.equal(rendered.includes('Sign out'), !local);
  });
}

test('configured accounts require login and preserve the requested destination', async () => {
  const tree = await component('App.tsx', 'AppRoutes', new Error('401'))();
  const route = tree.props.children.find(route => route.props.path === '/*');
  assert.equal(route.props.element.type, 'Navigate');
  assert.equal(route.props.element.props.to, '/login?from=%2Fprojects');
});
