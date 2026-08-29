import React from 'react';
import { MemoryRouter } from 'react-router-dom';
import { render, waitFor } from '@testing-library/react';
import App from './App';

describe('Components/App', () => {
  beforeEach(() => {
    jest.resetAllMocks();
  });

  test('renders without an error"', async () => {
    const { queryByText } = render(
      <MemoryRouter>
        <App />
      </MemoryRouter>
    );

    // Application is lazy loaded, so we need to wait for the component and routes to be rendered
    await waitFor(() => expect(queryByText(/AI 助理/i)).toBeInTheDocument(), { timeout: 2000 });
  });
});
