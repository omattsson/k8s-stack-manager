import axios from 'axios';
import { axiosConfig } from './config';

const api = axios.create(axiosConfig);

// Add response interceptor for error handling
api.interceptors.response.use(
  (response) => response,
  (error) => {
    console.error('API Error:', error);
    return Promise.reject(error);
  }
);

export default api;
