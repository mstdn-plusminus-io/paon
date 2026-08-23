import reducer from '../status_lists';

const QUOTES_FETCH_REQUEST = 'QUOTES_FETCH_REQUEST';
const QUOTES_FETCH_FAIL = 'QUOTES_FETCH_FAIL';

describe('Mastodon 4.5 quote status list', () => {
  it('clears the per-status loading flag after a fetch error', () => {
    let state = reducer(undefined, { type: '@@INIT' });
    state = reducer(state, { type: QUOTES_FETCH_REQUEST, id: '42' });
    expect(state.getIn(['quotes', '42', 'isLoading'])).toBe(true);

    state = reducer(state, { type: QUOTES_FETCH_FAIL, id: '42' });
    expect(state.getIn(['quotes', '42', 'isLoading'])).toBe(false);
    expect(state.hasIn(['quotes', 'isLoading'])).toBe(false);
  });
});
