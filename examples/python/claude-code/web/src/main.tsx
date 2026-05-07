import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { loadConfig } from './config';
import App from './App';

loadConfig().then(() => {
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <App />
    </StrictMode>
  );
});
